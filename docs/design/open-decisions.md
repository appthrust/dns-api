# Open Decisions

This section tracks unresolved design decisions. When an item is resolved, reflect the current design in the relevant section and remove it from this list.

- [ ] Define conformance test distribution, execution method, configuration file schema, and support level representation.
- [ ] Define shared ownership of hosted zones by multiple platforms and ownership tag naming.
- [ ] Define non-Go devbox tool versions.
- [ ] Define the operator-controlled recursive view authoritative for each external deployment's clients and members. Cluster mode proves only the PMC view; dns-api does not infer a shared split-horizon view.
- [ ] Validate the external provider's 16-NS/2-address fan-out bounds and committed defaults (10-second name deadline from limiter admission; manager-wide 40 QPS / burst 80, with per-Zone NS/SOA and NS-host work amortized per probe cycle) against expected Zone sizes and DNS topologies. Larger configurations fail closed or incur queue delay, never relax exact-set verification.
- [ ] Assign coordinated CRD-carrier, admission/TLS, and chart upgrade ownership in consuming installations. External requires cert-manager-enabled TLS and synchronized optional-identity UI/CRDs; Provider enablement alone does not establish readiness.
