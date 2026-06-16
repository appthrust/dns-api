import React from 'react';
import { HashRouter, Link, Redirect, Route, Switch } from 'react-router-dom';
import {
  DnsApiApp,
  OverviewPage,
  PlatformIntegrationDeletePage,
  PlatformIntegrationDetailPage,
  PlatformIntegrationEditPage,
  PlatformIntegrationsPage,
  PlatformIntegrationTypeSelectPage,
  PlatformPage,
  PlatformCloudflareIntegrationNewPage,
  PlatformRoute53IntegrationNewPage,
  PlatformZoneClassDeletePage,
  PlatformZoneClassDetailPage,
  PlatformZoneClassEditPage,
  PlatformZoneClassesPage,
  PlatformZoneClassIdentitySelectPage,
  PlatformZoneClassNewPage,
  RecordSetDeleteRoutePage,
  RecordSetDetailRoutePage,
  RecordSetEditPage,
  RecordSetNewPage,
  ZoneClassSelectPage,
  ZoneDeletePage,
  ZoneDetailPage,
  ZoneEditPage,
  ZoneNewPage,
  ZonesPage,
} from '@appthrust/dns-api-ui';
import { createMockPlatform, type MockScenario } from './mockPlatform';
import { mockTheme } from './mockTheme';

const scenarios: Array<{ value: MockScenario; label: string }> = [
  { value: 'data', label: 'Data' },
  { value: 'empty', label: 'Empty' },
  { value: 'rbac-denied', label: 'RBAC denied' },
  { value: 'admission-error', label: 'Admission error' },
  { value: 'warning-condition', label: 'Warning condition' },
  { value: 'provider-pending', label: 'Provider pending' },
  { value: 'conflict', label: 'Conflict' },
  { value: 'delete-blocked', label: 'Delete blocked' },
];

function Navigation({
  scenario,
  onScenarioChange,
}: {
  scenario: MockScenario;
  onScenarioChange: (scenario: MockScenario) => void;
}) {
  return (
    <nav className="mock-nav">
      <Link to="/dns">Overview</Link>
      <Link to="/dns/zones">Zones</Link>
      <Link to="/dns/platform/zoneclasses">Zone Classes</Link>
      <Link to="/dns/platform/integrations">Provider Identities</Link>
      <label className="mock-scenario">
        <span>State</span>
        <select
          value={scenario}
          onChange={event => onScenarioChange(event.target.value as MockScenario)}
        >
          {scenarios.map(item => (
            <option key={item.value} value={item.value}>
              {item.label}
            </option>
          ))}
        </select>
      </label>
    </nav>
  );
}

export function MockShell() {
  const [scenario, setScenario] = React.useState<MockScenario>('data');
  const platform = React.useMemo(() => createMockPlatform(scenario), [scenario]);

  React.useEffect(() => {
    if (document.body.style.pointerEvents === 'none') {
      document.body.style.pointerEvents = '';
    }
  }, [scenario]);

  return (
    <HashRouter>
      <DnsApiApp key={scenario} platform={platform} theme={mockTheme}>
        <Navigation scenario={scenario} onScenarioChange={setScenario} />
        <Switch>
          <Route exact path="/dns" component={OverviewPage} />
          <Route exact path="/dns/zones" component={ZonesPage} />
          <Route exact path="/dns/zones/new" component={ZoneClassSelectPage} />
          <Route exact path="/dns/zones/new/:zoneClassNamespace/:zoneClassName" component={ZoneNewPage} />
          <Route exact path="/dns/zones/:namespace/:name/recordsets/new" component={RecordSetNewPage} />
          <Route
            exact
            path="/dns/zones/:namespace/:name/recordsets/:recordNamespace/:recordName/edit"
            component={RecordSetEditPage}
          />
          <Route
            exact
            path="/dns/zones/:namespace/:name/recordsets/:recordNamespace/:recordName/delete"
            component={RecordSetDeleteRoutePage}
          />
          <Route
            exact
            path="/dns/zones/:namespace/:name/recordsets/:recordNamespace/:recordName"
            component={RecordSetDetailRoutePage}
          />
          <Route exact path="/dns/zones/:namespace/:name/edit" component={ZoneEditPage} />
          <Route exact path="/dns/zones/:namespace/:name/delete" component={ZoneDeletePage} />
          <Route exact path="/dns/zones/:namespace/:name" component={ZoneDetailPage} />
          <Route exact path="/dns/platform" component={PlatformPage} />
          <Route exact path="/dns/platform/integrations" component={PlatformIntegrationsPage} />
          <Route exact path="/dns/platform/integrations/new" component={PlatformIntegrationTypeSelectPage} />
          <Route
            exact
            path="/dns/platform/integrations/new/route53"
            component={PlatformRoute53IntegrationNewPage}
          />
          <Route
            exact
            path="/dns/platform/integrations/new/cloudflare"
            component={PlatformCloudflareIntegrationNewPage}
          />
          <Route
            exact
            path="/dns/platform/integrations/:namespace/:name/edit"
            component={PlatformIntegrationEditPage}
          />
          <Route
            exact
            path="/dns/platform/integrations/:namespace/:name/delete"
            component={PlatformIntegrationDeletePage}
          />
          <Route
            exact
            path="/dns/platform/integrations/:namespace/:name"
            component={PlatformIntegrationDetailPage}
          />
          <Route exact path="/dns/platform/zoneclasses" component={PlatformZoneClassesPage} />
          <Route exact path="/dns/platform/zoneclasses/new" component={PlatformZoneClassIdentitySelectPage} />
          <Route
            exact
            path="/dns/platform/zoneclasses/new/:identityNamespace/:identityName"
            component={PlatformZoneClassNewPage}
          />
          <Route
            exact
            path="/dns/platform/zoneclasses/:namespace/:name/edit"
            component={PlatformZoneClassEditPage}
          />
          <Route
            exact
            path="/dns/platform/zoneclasses/:namespace/:name/delete"
            component={PlatformZoneClassDeletePage}
          />
          <Route
            exact
            path="/dns/platform/zoneclasses/:namespace/:name"
            component={PlatformZoneClassDetailPage}
          />
          <Redirect to="/dns" />
        </Switch>
      </DnsApiApp>
    </HashRouter>
  );
}
