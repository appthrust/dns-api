local_resource(
    'generated-manifests',
    'task manifests',
    deps=[
        'pkg/go/api/dns/v1alpha1',
        'pkg/go/api/route53/v1alpha1',
        'internal/go/core',
        'internal/go/providers',
        'internal/go/core/webhook',
        'Taskfile.yml',
    ],
)

local_resource(
    'cert-manager',
    '''
helm repo add jetstack https://charts.jetstack.io
helm repo update
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --set crds.enabled=true \
  --wait
''',
)

local_resource(
    'dns-api-webhook-cert',
    'kustomize build app/operator/config/certmanager | kubectl apply -f -',
    deps=[
        'app/operator/config/certmanager',
    ],
    resource_deps=['cert-manager'],
)

docker_build(
    'dns-api-controller',
    '.',
    dockerfile='Dockerfile',
)

def kustomize_build(path):
    return local('kustomize build ' + path)

k8s_yaml(kustomize_build('app/operator/config/namespace'))
k8s_yaml(kustomize_build('app/operator/config/crd'))
k8s_yaml(kustomize_build('app/operator/config/rbac'))
k8s_yaml(kustomize_build('app/operator/config/provider'))
k8s_yaml(kustomize_build('app/operator/config/manager'))
k8s_yaml(kustomize_build('app/operator/config/webhook'))

k8s_resource('dns-api-controller-manager', resource_deps=['dns-api-webhook-cert'])
