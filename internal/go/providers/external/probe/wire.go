package probe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/appthrust/dns-api/internal/go/providers/external/config"
	externalv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/external/v1alpha1"
	"github.com/miekg/dns"
	"golang.org/x/time/rate"
)

const (
	DefaultQPS         = 40
	DefaultBurst       = 80
	MaxConcurrentNames = 4
	ExchangeTimeout    = 2 * time.Second
	NameTimeout        = 10 * time.Second
	MaxCNAMEs          = 8
)

type WireOptions struct {
	QPS        float64
	Burst      int
	ResolvConf string
}

type WireResolver struct {
	limiter    *rate.Limiter
	slots      chan struct{}
	resolvConf string
}

func NewWireResolver(options WireOptions) (*WireResolver, error) {
	if options.QPS == 0 {
		options.QPS = DefaultQPS
	}
	if options.Burst == 0 {
		options.Burst = DefaultBurst
	}
	if options.QPS <= 0 || math.IsNaN(options.QPS) || math.IsInf(options.QPS, 0) || options.Burst < 1 {
		return nil, fmt.Errorf("external DNS QPS and burst must be positive and finite")
	}
	if options.ResolvConf == "" {
		options.ResolvConf = "/etc/resolv.conf"
	}
	return &WireResolver{limiter: rate.NewLimiter(rate.Limit(options.QPS), options.Burst), slots: make(chan struct{}, MaxConcurrentNames), resolvConf: options.ResolvConf}, nil
}

// EffectivePolicy rereads resolv.conf each reconcile so changes invalidate receipts.
func (r *WireResolver) EffectivePolicy(policy externalv1alpha1.ExternalZoneClassParameters) (externalv1alpha1.ExternalZoneClassParameters, error) {
	policy.Resolver.Servers = slices.Clone(policy.Resolver.Servers)
	if policy.Resolver.Mode == "" {
		policy.Resolver.Mode = externalv1alpha1.ResolverModeCluster
	}
	if policy.Resolver.Mode == externalv1alpha1.ResolverModeCluster {
		conf, err := dns.ClientConfigFromFile(r.resolvConf)
		if err != nil {
			return policy, errors.New("resolver-config")
		}
		policy.Resolver.Servers = nil
		for _, host := range conf.Servers {
			ip, err := netip.ParseAddr(host)
			if err != nil || ip.Zone() != "" {
				return policy, errors.New("resolver-config")
			}
			policy.Resolver.Servers = append(policy.Resolver.Servers, net.JoinHostPort(ip.String(), conf.Port))
		}
		slices.Sort(policy.Resolver.Servers)
		policy.Resolver.Servers = slices.Compact(policy.Resolver.Servers)
	}
	if config.ValidateServers(policy.Resolver.Servers) != nil {
		return policy, errors.New("resolver-config")
	}
	slices.Sort(policy.Resolver.Servers)
	return policy, nil
}

func (r *WireResolver) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	if config.ValidateServers(request.Policy.Resolver.Servers) != nil {
		return ProbeResult{}, errors.New("resolver-config")
	}
	queued := time.Now()
	if !request.DueTime.IsZero() {
		queued = request.DueTime
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		return ProbeResult{}, ctx.Err()
	}
	// Admit the first exchange before starting the name deadline.
	if err := r.limiter.Wait(ctx); err != nil {
		return ProbeResult{}, err
	}
	ObserveQueueDelay(time.Since(queued))
	ctx, cancel := context.WithTimeout(ctx, NameTimeout)
	defer cancel()
	start := time.Now()
	session := wireSession{resolver: r, firstToken: true, authorityAnswers: map[questionKey]*dns.Msg{}, qtypes: map[uint16]int{}}
	var result ProbeResult
	if request.ZoneOnly {
		result.Zone = session.zone(ctx, request)
	} else {
		for _, server := range request.Policy.Resolver.Servers {
			result.Views = append(result.Views, session.resolve(ctx, request.Name, server, "", request.Zone))
		}
		if len(request.Policy.ExpectedNameservers) != 0 {
			if request.Evidence == nil || request.Evidence.Zone != request.Zone || request.Evidence.PolicyFingerprint != Fingerprint(request.Policy) || len(request.Evidence.Authorities) == 0 {
				return ProbeResult{}, errors.New("invalid-response")
			}
			for _, authority := range request.Evidence.Authorities {
				view := session.resolve(ctx, request.Name, request.Policy.Resolver.Servers[0], authority, request.Zone)
				result.Views = append(result.Views, view)
				foreign := request.WithdrawalAddress != ""
				for _, target := range view.Chain {
					if target != request.Zone && !strings.HasSuffix(target, "."+request.Zone) {
						foreign = true
						break
					}
				}
				if foreign {
					for _, server := range request.Policy.Resolver.Servers[1:] {
						result.Views = append(result.Views, session.resolve(ctx, request.Name, server, authority, request.Zone))
					}
				}
			}
		}
	}
	decision := Evaluate(request, result)
	if request.ZoneOnly {
		decision = EvaluateZone(request, result.Zone)
	} else if request.WithdrawalAddress != "" {
		decision = EvaluateWithdrawal(request, result).Decision
	}
	outcome := decision.Outcome()
	for qtype, count := range session.qtypes {
		probes.WithLabelValues(outcome, dns.TypeToString[qtype]).Add(float64(count))
	}
	duration.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
	return result, nil
}

type wireSession struct {
	resolver         *WireResolver
	firstToken       bool
	authorityAnswers map[questionKey]*dns.Msg
	qtypes           map[uint16]int
}

type questionKey struct {
	server, name string
	qtype        uint16
}

func (s *wireSession) exchange(ctx context.Context, server, name string, qtype uint16, recursive bool) (*dns.Msg, string) {
	key := questionKey{server, name, qtype}
	if !recursive {
		if reply := s.authorityAnswers[key]; reply != nil {
			return reply, ""
		}
	}
	if s.firstToken {
		s.firstToken = false
	} else if err := s.resolver.limiter.Wait(ctx); err != nil {
		return nil, "timeout"
	}
	request := new(dns.Msg)
	request.SetQuestion(dns.Fqdn(name), qtype)
	request.RecursionDesired = recursive
	request.Opcode = dns.OpcodeQuery
	if s.qtypes != nil {
		s.qtypes[qtype]++
	}
	client := &dns.Client{Net: "udp", Timeout: ExchangeTimeout}
	reply, _, err := client.ExchangeContext(ctx, request, server)
	if err == nil && reply != nil && reply.Truncated {
		if err = s.resolver.limiter.Wait(ctx); err == nil {
			client.Net = "tcp"
			reply, _, err = client.ExchangeContext(ctx, request, server)
		}
	}
	failure := ""
	if err != nil {
		failure = "transport"
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			failure = "timeout"
		}
	} else if reply == nil || reply.Truncated || !reply.Response || reply.Opcode != dns.OpcodeQuery || len(reply.Question) != 1 || reply.Question[0] != request.Question[0] {
		failure = "invalid-response"
	} else {
		switch reply.Rcode {
		case dns.RcodeSuccess, dns.RcodeNameError:
		case dns.RcodeServerFailure:
			failure = "SERVFAIL"
		case dns.RcodeRefused:
			failure = "REFUSED"
		default:
			failure = "invalid-response"
		}
	}
	if failure == "" && !recursive && s.authorityAnswers != nil {
		s.authorityAnswers[key] = reply
	}
	return reply, failure
}

func (s *wireSession) resolve(ctx context.Context, name, recursive, authority, zone string) View {
	view := View{ID: "recursive/" + recursive}
	if authority != "" {
		view.ID = "authority/" + authority + "/via/" + recursive
	}
	current := CanonicalName(name)
	seen := map[string]bool{}
	for {
		if seen[current] {
			view.Mismatch = true
			return view
		}
		seen[current] = true
		server, rd := recursive, true
		if authority != "" && (current == zone || strings.HasSuffix(current, "."+zone)) {
			server, rd = authority, false
		}
		var targets, a, aaaa []string
		rcodes := [2]int{}
		for i, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
			reply, failure := s.exchange(ctx, server, current, qtype, rd)
			if failure != "" {
				view.Failure = failure
				return view
			}
			if !rd && !reply.Authoritative {
				view.Failure = "invalid-response"
				return view
			}
			if !rd {
				view.Authoritative = true
			}
			rcodes[i] = reply.Rcode
			view.Answers = append(view.Answers, answerEvidence(reply, current, qtype))
			ownerAnswers := 0
			for _, rr := range reply.Answer {
				if CanonicalName(rr.Header().Name) != current {
					continue
				}
				ttl := rr.Header().Ttl
				switch value := rr.(type) {
				case *dns.CNAME:
					targets = append(targets, CanonicalName(value.Target))
					ownerAnswers++
				case *dns.A:
					a = append(a, value.A.String())
					ownerAnswers++
				case *dns.AAAA:
					aaaa = append(aaaa, value.AAAA.String())
					ownerAnswers++
				default:
					continue
				}
				view.TTL = minTTL(view.TTL, &ttl)
			}
			if reply.Rcode == dns.RcodeNameError || ownerAnswers == 0 {
				view.NegativeTTL = minTTL(view.NegativeTTL, negativeTTL(reply))
			}
		}
		if rcodes[0] != rcodes[1] {
			view.Failure = "invalid-response"
			return view
		}
		view.RCode = rcodes[0]
		targets = Names(targets)
		if len(targets) == 0 {
			view.A, view.AAAA = Addresses(a), Addresses(aaaa)
			return view
		}
		if len(targets) != 1 || len(a)+len(aaaa) != 0 {
			view.Mismatch = true
			view.Chain = append(view.Chain, targets...)
			return view
		}
		view.Chain = append(view.Chain, targets[0])
		if len(view.Chain) > MaxCNAMEs {
			view.Failure = "chain-limit"
			return view
		}
		current = targets[0]
	}
}

func negativeTTL(reply *dns.Msg) *uint32 {
	var ttl *uint32
	for _, rr := range reply.Ns {
		if soa, ok := rr.(*dns.SOA); ok {
			value := min(soa.Hdr.Ttl, soa.Minttl)
			ttl = minTTL(ttl, &value)
		}
	}
	return ttl
}

func (s *wireSession) apex(ctx context.Context, server, zone string, rd bool) AuthorityObservation {
	view := AuthorityObservation{ID: "recursive/" + server}
	if !rd {
		view.ID = "authority/" + server
	}
	for _, qtype := range []uint16{dns.TypeNS, dns.TypeSOA} {
		reply, failure := s.exchange(ctx, server, zone, qtype, rd)
		if failure != "" {
			view.Failure = failure
			return view
		}
		if !rd && !reply.Authoritative {
			view.Failure = "invalid-response"
			return view
		}
		view.Authoritative = reply.Authoritative
		view.NegativeTTL = minTTL(view.NegativeTTL, negativeTTL(reply))
		for _, rr := range reply.Answer {
			if CanonicalName(rr.Header().Name) != zone {
				continue
			}
			ttl := rr.Header().Ttl
			switch value := rr.(type) {
			case *dns.NS:
				view.Nameservers = append(view.Nameservers, CanonicalName(value.Ns))
			case *dns.SOA:
				view.SOAOwner = CanonicalName(value.Hdr.Name)
			default:
				continue
			}
			view.TTL = minTTL(view.TTL, &ttl)
		}
	}
	view.Nameservers = Names(view.Nameservers)
	return view
}

func (s *wireSession) zone(ctx context.Context, request ProbeRequest) *ZoneEvidence {
	evidence := &ZoneEvidence{Zone: request.Zone, PolicyFingerprint: Fingerprint(request.Policy)}
	if len(request.Policy.ExpectedNameservers) == 0 {
		return evidence
	}
	for _, server := range request.Policy.Resolver.Servers {
		evidence.Views = append(evidence.Views, s.apex(ctx, server, request.Zone, true))
	}
	// A wrong delegation is completed evidence; never send to unverified authority destinations.
	if evaluateZone(request, evidence, false).Status != "True" {
		return evidence
	}
	for _, host := range request.Policy.ExpectedNameservers {
		var addresses []string
		for viewIndex, server := range request.Policy.Resolver.Servers {
			view := s.resolve(ctx, host, server, "", "")
			if view.Failure != "" || view.Mismatch || view.RCode != dns.RcodeSuccess || len(view.A)+len(view.AAAA) == 0 {
				failure := view.Failure
				if failure == "" {
					failure = "invalid-response"
				}
				evidence.Views = append(evidence.Views, AuthorityObservation{ID: view.ID, Failure: failure})
				return evidence
			}
			evidence.Views[viewIndex].TTL = minTTL(evidence.Views[viewIndex].TTL, view.TTL)
			addresses = append(addresses, view.A...)
			addresses = append(addresses, view.AAAA...)
		}
		addresses = Addresses(addresses)
		if len(addresses) > 2 {
			evidence.Views = append(evidence.Views, AuthorityObservation{ID: "authority/" + host, Failure: "probe-limit"})
			return evidence
		}
		for _, address := range addresses {
			evidence.Authorities = append(evidence.Authorities, net.JoinHostPort(address, "53"))
		}
	}
	slices.Sort(evidence.Authorities)
	evidence.Authorities = slices.Compact(evidence.Authorities)
	for _, authority := range evidence.Authorities {
		evidence.Views = append(evidence.Views, s.apex(ctx, authority, request.Zone, false))
	}
	return evidence
}
