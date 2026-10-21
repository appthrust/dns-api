package zoneunit

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/appthrust/dns-api/internal/go/core/declaration"
	probe "github.com/appthrust/dns-api/internal/go/core/dnsprobe"
	"github.com/appthrust/dns-api/internal/go/core/providercontract"
	"github.com/appthrust/dns-api/internal/go/providers/external/config"
	"github.com/appthrust/dns-api/internal/go/providers/external/verification"
	dnsv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/dns/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Options struct {
	ControllerName  string
	ProviderName    string
	ProviderVersion string
	QPS             float64
	Burst           int
	Resolver        probe.Resolver
	Clock           func() time.Time
	// Jitter returns a value in [-1,1], scaled to +/-10 percent.
	Jitter func() float64
	// EffectivePolicy resolves pod DNS without using the OS DNS lookup fallback.
	EffectivePolicy func(probe.Policy) (probe.Policy, error)
}

type ZoneReconciler struct {
	client.Client
	Options Options
	// Reader is a live read boundary for rejecting results whose inputs changed during DNS I/O.
	Reader client.Reader
}

func NewReconciler(k8sClient client.Client, options Options) (*ZoneReconciler, error) {
	if options.Resolver == nil {
		wire, err := probe.NewWireResolver(probe.WireOptions{QPS: options.QPS, Burst: options.Burst, Provider: "external"})
		if err != nil {
			return nil, err
		}
		options.Resolver = wire
		if options.EffectivePolicy == nil {
			options.EffectivePolicy = wire.EffectivePolicy
		}
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Jitter == nil {
		options.Jitter = func() float64 { return rand.Float64()*2 - 1 }
	}
	return &ZoneReconciler{Client: k8sClient, Reader: k8sClient, Options: options}, nil
}

type environment struct {
	class       dnsv1alpha1.ZoneClass
	provider    dnsv1alpha1.Provider
	policy      probe.Policy
	fingerprint string
	policyError error
}

type acceptance struct {
	Status          metav1.ConditionStatus
	Reason, Message string
}

func (r *ZoneReconciler) observe(ctx context.Context, c client.Client, unit *dnsv1alpha1.ZoneUnit) (*environment, *acceptance, error) {
	env := &environment{}
	ref := unit.Spec.Zone.ZoneClassRef
	if err := c.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &env.class); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &acceptance{metav1.ConditionFalse, "InvalidZoneClassRef", "referenced ZoneClass was not found"}, nil
		}
		return nil, nil, err
	}
	if env.class.Spec.ControllerName != config.ControllerName(r.Options.ControllerName) {
		return nil, nil, nil
	}
	binding := config.Binding(r.Options.ProviderName, r.Options.ProviderVersion)
	if unit.Spec.Provider != binding || env.class.Spec.Provider.Name != binding.Name {
		return nil, &acceptance{metav1.ConditionFalse, "InvalidProvider", "ZoneUnit provider is not handled by external controller"}, nil
	}
	if err := c.Get(ctx, client.ObjectKey{Name: binding.Name}, &env.provider); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &acceptance{metav1.ConditionFalse, "InvalidProvider", "referenced Provider was not found"}, nil
		}
		return nil, nil, err
	}
	version, served := providercontract.ServedVersion(&env.provider, env.class.Spec.Provider.Version)
	storage, resolved := providercontract.StorageVersion(&env.provider)
	if !resolved {
		return nil, &acceptance{metav1.ConditionUnknown, "ProviderStorageVersionNotResolved", "provider storage version is not resolved"}, nil
	}
	if !served || storage.Name != binding.Version || version.Identity != nil || storage.Identity != nil {
		return nil, &acceptance{metav1.ConditionFalse, "InvalidProvider", "external provider requires its configured credentialless storage version"}, nil
	}
	if config.HasIdentity(&env.class) {
		return nil, &acceptance{metav1.ConditionFalse, "InvalidIdentityRef", "spec.identityRef must be omitted for external DNS"}, nil
	}
	condition := meta.FindStatusCondition(env.class.Status.Conditions, "Accepted")
	if !env.class.DeletionTimestamp.IsZero() || condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != env.class.Generation {
		return nil, &acceptance{metav1.ConditionFalse, "ZoneClassNotAccepted", "referenced ZoneClass is not accepted"}, nil
	}
	params, err := config.Parameters(&env.provider, version, &env.class)
	if err != nil {
		return nil, &acceptance{metav1.ConditionFalse, "InvalidProviderPayload", "external resolver parameters are invalid"}, nil
	}
	if allowed, err := declaration.ZoneAllowedByClass(ctx, c, &env.class, unit.Spec.Zone.Ref.Namespace); err != nil {
		return nil, nil, err
	} else if !allowed {
		return nil, &acceptance{metav1.ConditionFalse, "NotAllowedByZoneClass", "Zone namespace is not allowed by ZoneClass"}, nil
	}
	if !emptyObject(unit.Spec.Zone.Adoption) {
		return nil, &acceptance{metav1.ConditionFalse, "InvalidProviderPayload", "external DNS does not support adoption"}, nil
	}
	env.policy = config.ProbePolicy(params)
	if r.Options.EffectivePolicy != nil {
		env.policy, env.policyError = r.Options.EffectivePolicy(env.policy)
	}
	namespaceNames := []string{unit.Spec.Zone.Ref.Namespace}
	for _, item := range unit.Spec.RecordSets {
		namespaceNames = append(namespaceNames, item.RecordSetNamespace)
	}
	for _, item := range unit.Spec.Withdrawals {
		namespaceNames = append(namespaceNames, item.ClaimNamespace)
	}
	slices.Sort(namespaceNames)
	namespaceNames = slices.Compact(namespaceNames)
	namespacePolicies := make(map[string]map[string]string, len(namespaceNames))
	for _, name := range namespaceNames {
		var namespace corev1.Namespace
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &namespace); err != nil && !apierrors.IsNotFound(err) {
			return nil, nil, err
		}
		namespacePolicies[name] = namespace.Labels
	}
	env.fingerprint = probe.Fingerprint(struct {
		ClassUID          string
		ClassSpec         dnsv1alpha1.ZoneClassSpec
		ProviderUID       string
		ProviderSpec      dnsv1alpha1.ProviderSpec
		UnitUID           string
		Zone              dnsv1alpha1.ZoneUnitZoneSpec
		Policy            probe.Policy
		NamespacePolicies map[string]map[string]string
	}{string(env.class.UID), env.class.Spec, string(env.provider.UID), env.provider.Spec, string(unit.UID), unit.Spec.Zone, env.policy, namespacePolicies})
	return env, nil, nil
}

func (r *ZoneReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var unit dnsv1alpha1.ZoneUnit
	if err := r.Get(ctx, request.NamespacedName, &unit); err != nil {
		if apierrors.IsNotFound(err) {
			verification.SetPending(request.NamespacedName.String(), nil)
			verification.SetWithdrawalsPending(request.NamespacedName.String(), nil)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if unit.Spec.Provider.Name != config.Binding(r.Options.ProviderName, r.Options.ProviderVersion).Name {
		return ctrl.Result{}, nil
	}
	env, rejected, err := r.observe(ctx, r.Client, &unit)
	if err != nil {
		return ctrl.Result{}, err
	}
	if env == nil && rejected == nil {
		return ctrl.Result{}, nil
	}
	base := unit.DeepCopy()
	prepareReceipts(&unit)
	if !unit.DeletionTimestamp.IsZero() {
		// No provider finalizer is necessary: there is no external object to delete.
		for i := range unit.Status.RecordSets {
			unit.Status.RecordSets[i].DeletionCompleted = unit.Spec.RecordSets[i].RecordSetUID != ""
		}
		verification.SetPending(request.NamespacedName.String(), nil)
		if len(unit.Spec.Withdrawals) == 0 {
			verification.SetWithdrawalsPending(request.NamespacedName.String(), nil)
			return ctrl.Result{}, r.patch(ctx, base, &unit)
		}
	}
	if rejected != nil {
		r.reject(&unit, *rejected)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.patch(ctx, base, &unit)
	}
	now := r.Options.Clock()
	state := loadState(&unit, env.fingerprint)
	groups := groupNames(&unit, env.fingerprint)
	withdrawalsChanged := prepareWithdrawals(&unit, &state, env, now)
	active := make(map[string]bool, len(groups))
	for _, group := range groups {
		active[group.Name] = true
		currentReceipt := true
		for _, index := range group.Items {
			item := &unit.Status.RecordSets[index]
			condition := meta.FindStatusCondition(item.Conditions, "Programmed")
			if condition == nil || condition.ObservedGeneration != item.ObservedGeneration {
				currentReceipt = false
			}
			if meta.FindStatusCondition(item.Conditions, "Accepted") == nil {
				setCondition(&item.Conditions, "Accepted", metav1.ConditionTrue, "Accepted", "External DNS record intent is accepted.", item.ObservedGeneration, now)
			}
		}
		if !currentReceipt || state.Names[group.Name].Fingerprint != group.Fingerprint {
			delete(state.Names, group.Name)
			for _, index := range group.Items {
				resetProgrammed(&unit.Status.RecordSets[index], now)
			}
		}
	}
	prunedSchedule := false
	for name := range state.Names {
		if !active[name] {
			delete(state.Names, name)
			prunedSchedule = true
		}
	}
	due := selectDue(groups, state, now)
	withdrawalDue := selectWithdrawalDue(&unit, state, env.fingerprint, now, MaxNamesPerPass)
	if len(due) != 0 && len(withdrawalDue) != 0 {
		// Reserve half of a busy pass for each class of work; both have fair cursors.
		withdrawalDue = selectWithdrawalDue(&unit, state, env.fingerprint, now, MaxNamesPerPass/2)
		due = due[:min(len(due), MaxNamesPerPass-withdrawalProbeCost(&unit, state, withdrawalDue))]
	}
	zoneCondition := meta.FindStatusCondition(unit.Status.Zone.Conditions, "Programmed")
	zoneDue := state.Zone.Fingerprint != env.fingerprint || !state.Zone.Next.After(now) || zoneCondition == nil || zoneCondition.ObservedGeneration != unit.Spec.Zone.ObservedGeneration
	var evidence *probe.ZoneEvidence
	zoneDecision := currentDecision(unit.Status.Zone.Conditions)
	zoneProbed := zoneDue || ((len(due) != 0 || len(withdrawalDue) != 0) && zoneDecision.Status == metav1.ConditionTrue)
	if zoneProbed {
		zoneRequest := probe.ProbeRequest{Zone: unit.Spec.Zone.DomainName, Name: unit.Spec.Zone.DomainName, Policy: env.policy, ZoneOnly: true}
		switch {
		case env.policyError != nil:
			zoneDecision = probe.Failed(zoneRequest.Zone, "configured-resolvers", "resolver-config")
		case len(env.policy.ExpectedNameservers) == 0:
			zoneDecision = probe.EvaluateZone(zoneRequest, nil)
		default:
			observed, probeErr := r.Options.Resolver.Probe(ctx, zoneRequest)
			evidence = observed.Zone
			zoneDecision = probe.EvaluateZone(zoneRequest, evidence)
			if probeErr != nil {
				zoneDecision = probe.Failed(zoneRequest.Zone, "required-views", failureCode(probeErr))
			}
		}
		completed := r.Options.Clock()
		state.Zone = advance(state.Zone, env.fingerprint, zoneDecision, completed, r.Options.Jitter())
		setZoneResult(&unit, env.policy, zoneDecision, completed, state.Zone.Next)
	}
	invalidGroups := map[string]bool{}
	for _, groupIndex := range due {
		group := groups[groupIndex]
		old := state.Names[group.Name]
		dueTime := old.Next
		if dueTime.IsZero() {
			dueTime = now
		}
		valid, reason, message := validateGroup(&unit, group)
		decision := zoneDecision
		if !valid {
			invalidGroups[group.Name] = true
			for _, index := range group.Items {
				setCondition(&unit.Status.RecordSets[index].Conditions, "Accepted", metav1.ConditionFalse, reason, message, unit.Spec.RecordSets[index].ObservedGeneration, now)
				resetProgrammed(&unit.Status.RecordSets[index], now)
			}
			decision = probe.Decision{Status: metav1.ConditionUnknown, Reason: "Reconciling", Message: message}
		} else if zoneDecision.Status == metav1.ConditionTrue {
			probeRequest := probe.ProbeRequest{Name: group.CheckedName, Zone: unit.Spec.Zone.DomainName, Expected: group.Expected, Policy: env.policy, Evidence: evidence, DueTime: dueTime}
			observed, probeErr := r.Options.Resolver.Probe(ctx, probeRequest)
			decision = verification.Evaluate(probeRequest, observed)
			retainPublication(&state, group, probeRequest, observed, r.Options.Clock())
			if probeErr != nil {
				decision = probe.Failed(group.CheckedName, "required-views", failureCode(probeErr))
			}
		}
		completed := r.Options.Clock()
		scheduled := advance(old, group.Fingerprint, decision, completed, r.Options.Jitter())
		state.Names[group.Name] = scheduled
		if valid {
			setGroupResult(&unit, group, env.policy, decision, completed, scheduled.Next)
		}
		state.Cursor = group.Name
	}
	if zoneProbed && zoneDecision.Status != metav1.ConditionTrue {
		for _, group := range groups {
			if invalidGroups[group.Name] {
				continue
			}
			// Withdraw every old success immediately, including names outside this pass.
			for _, index := range group.Items {
				item := &unit.Status.RecordSets[index]
				item.Provider = publicData(env.policy, group.CheckedName, group.SampledWildcard, zoneDecision, r.Options.Clock(), state.Zone.Next)
				setCondition(&item.Conditions, "Programmed", zoneDecision.Status, zoneDecision.Reason, zoneDecision.Message, item.ObservedGeneration, r.Options.Clock())
			}
		}
	}
	r.probeWithdrawals(ctx, &unit, &state, env, evidence, zoneDecision, withdrawalDue)
	if zoneProbed && zoneDecision.Status != metav1.ConditionTrue {
		invalidateWithdrawalZone(&unit, &state, r.Options.Clock())
		withdrawalsChanged = true
	}
	for name, publication := range state.Publications {
		if !active[name] && !publicationUnexpired(publication, r.Options.Clock()) {
			delete(state.Publications, name)
			prunedSchedule = true
		}
	}
	// DNS I/O is an asynchronous observation boundary. Live inputs must still agree.
	reader := readerClient{Client: r.Client, reader: r.Reader}
	var current dnsv1alpha1.ZoneUnit
	if err := reader.Get(ctx, request.NamespacedName, &current); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if current.UID != unit.UID || current.Generation != unit.Generation || !equality.Semantic.DeepEqual(current.Spec, unit.Spec) || !current.DeletionTimestamp.Equal(unit.DeletionTimestamp) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	fresh, blocked, err := r.observe(ctx, reader, &current)
	if err != nil {
		return ctrl.Result{}, err
	}
	if blocked != nil || fresh == nil || fresh.fingerprint != env.fingerprint || (fresh.policyError != nil) != (env.policyError != nil) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	// Preserve the stored RawExtension unless scheduling actually changed.
	// Re-encoding unchanged state can reorder API-normalized JSON keys and
	// make the status equality guard issue an otherwise empty patch.
	if zoneProbed || len(due) != 0 || len(withdrawalDue) != 0 || prunedSchedule || withdrawalsChanged {
		stateBytes, err := json.Marshal(state)
		if err != nil {
			return ctrl.Result{}, err
		}
		unit.Status.Provider = &dnsv1alpha1.ProviderStatus{State: runtime.RawExtension{Raw: stateBytes}}
	}
	setAggregate(&unit, r.Options.Clock())
	r.updatePending(&unit)
	next := min(nextRequeue(groups, state, r.Options.Clock()), nextWithdrawalRequeue(&unit, state, env.fingerprint, r.Options.Clock()))
	return ctrl.Result{RequeueAfter: next}, r.patch(ctx, base, &unit)
}

func validateGroup(unit *dnsv1alpha1.ZoneUnit, group nameGroup) (bool, string, string) {
	if group.Expected.CNAME != "" && len(group.Expected.A)+len(group.Expected.AAAA) != 0 {
		return false, "InvalidProviderPayload", "CNAME must not coexist with address records"
	}
	for _, index := range group.Items {
		item := unit.Spec.RecordSets[index]
		if !item.IsAllowed() || item.DeletionRequested || item.RecordSetUID == "" || item.ObservedGeneration <= 0 {
			return false, "DeniedByPolicy", "record claim is not allowed or not bound to a current incarnation"
		}
		if !emptyObject(item.Options) || !emptyObject(item.Adoption) || item.TTL == nil || *item.TTL < 1 {
			return false, "InvalidProviderPayload", "external DNS requires a standard TTL and no options or adoption"
		}
		valid := false
		switch item.Type {
		case dnsv1alpha1.RecordTypeA:
			valid = item.A != nil && len(item.A.Addresses) != 0 && item.AAAA == nil && item.CNAME == nil
		case dnsv1alpha1.RecordTypeAAAA:
			valid = item.AAAA != nil && len(item.AAAA.Addresses) != 0 && item.A == nil && item.CNAME == nil
		case dnsv1alpha1.RecordTypeCNAME:
			valid = item.CNAME != nil && item.CNAME.Target != "" && item.Name != "@" && item.A == nil && item.AAAA == nil
		}
		if !valid || item.TXT != nil || item.MX != nil || item.CAA != nil || item.NS != nil {
			return false, "InvalidProviderPayload", "external DNS supports only standard A, AAAA and CNAME bodies"
		}
	}
	return true, "", ""
}

func emptyObject(raw runtime.RawExtension) bool {
	if len(raw.Raw) == 0 {
		return true
	}
	var value map[string]json.RawMessage
	return json.Unmarshal(raw.Raw, &value) == nil && len(value) == 0
}

func (r *ZoneReconciler) patch(ctx context.Context, base, unit *dnsv1alpha1.ZoneUnit) error {
	unit.Status.ObservedGeneration = unit.Generation
	if equality.Semantic.DeepEqual(base.Status, unit.Status) {
		return nil
	}
	return r.Status().Patch(ctx, unit, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func failureCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	switch err.Error() {
	case "resolver-config", "invalid-response", "probe-limit", "chain-limit":
		return err.Error()
	}
	return "transport"
}

type readerClient struct {
	client.Client
	reader client.Reader
}

func (c readerClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.reader == nil {
		return c.Client.Get(ctx, key, obj, opts...)
	}
	return c.reader.Get(ctx, key, obj, opts...)
}
