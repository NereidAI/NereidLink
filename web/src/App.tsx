// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// App defines the route table. Every authenticated feature route renders
// inside AppLayout; unknown paths fall through to NotFoundPage. The single
// WebSocket connection is opened once for the application lifetime.

import { Navigate, Route, Routes } from "react-router-dom";

import AppLayout from "./components/AppLayout";
import RequireAuth from "./components/RequireAuth";
import AlarmsPage from "./pages/AlarmsPage";
import AccountSecurityPage from "./pages/AccountSecurityPage";
import CommandCenterPage from "./pages/CommandCenterPage";
import ProbesPage from "./pages/ProbesPage";
import DashboardsPage from "./pages/DashboardsPage";
import DashboardDetailPage from "./pages/DashboardDetailPage";
import BusinessOrchestrationPage from "./pages/BusinessOrchestrationPage";
import CustomersPage from "./pages/CustomersPage";
import DeviceServicesPage from "./pages/DeviceServicesPage";
import GatewayOperationsPage from "./pages/GatewayOperationsPage";
import SecretsPage from "./pages/SecretsPage";
import DeviceDetailPage from "./pages/DeviceDetailPage";
import DevicesPage from "./pages/DevicesPage";
import EscalationsPage from "./pages/EscalationsPage";
import FirmwarePage from "./pages/FirmwarePage";
import OtaPage from "./pages/OtaPage";
import HelpCenterPage from "./pages/HelpCenterPage";
import FirstRunRoute from "./pages/FirstRunRoute";
import OIDCCallbackPage from "./pages/OIDCCallbackPage";
import NotificationsPage from "./pages/NotificationsPage";
import NotFoundPage from "./pages/NotFoundPage";
import OverviewPage from "./pages/OverviewPage";
import ProfilesPage from "./pages/ProfilesPage";
import ProfileDetailPage from "./pages/ProfileDetailPage";
import RuleChainsPage from "./pages/RuleChainsPage";
import ForwardersPage from "./pages/ForwardersPage";
import CompanionEndpointsPage from "./pages/CompanionEndpointsPage";
import RuleChainEditor from "./components/rule/RuleChainEditor";
import StatsPage from "./pages/StatsPage";
import StorageHooksPage from "./pages/StorageHooksPage";
import SystemTenantsPage from "./pages/SystemTenantsPage";
import SystemUsersPage from "./pages/SystemUsersPage";
import SystemAuditPage from "./pages/SystemAuditPage";
import SystemCAsPage from "./pages/SystemCAsPage";
import SystemLicensePage from "./pages/SystemLicensePage";
import OpsOrdersPage from "./pages/OpsOrdersPage";
import DLQPage from "./pages/DLQPage";

// App renders the public login route and the guarded application routes.
export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<FirstRunRoute />} />
      <Route path="/auth/callback" element={<OIDCCallbackPage />} />
      <Route
        element={
          <RequireAuth>
            <AppLayout />
          </RequireAuth>
        }
      >
        <Route index element={<Navigate to="/overview" replace />} />
        <Route path="/system/tenants" element={<SystemTenantsPage />} />
        <Route path="/system/users" element={<SystemUsersPage />} />
        <Route path="/system/audit" element={<SystemAuditPage />} />
        <Route path="/system/cas" element={<SystemCAsPage />} />
        <Route path="/system/license" element={<SystemLicensePage />} />
        <Route path="/system/ops-orders" element={<OpsOrdersPage />} />
        <Route path="/overview" element={<OverviewPage />} />
        <Route path="/dashboards" element={<DashboardsPage />} />
        <Route path="/dashboards/:dashboardId" element={<DashboardDetailPage />} />
        <Route path="/devices" element={<DevicesPage />} />
        <Route path="/devices/:deviceId" element={<DeviceDetailPage />} />
        <Route path="/customers" element={<CustomersPage />} />
        <Route path="/firmware" element={<FirmwarePage />} />
        <Route path="/ota" element={<OtaPage />} />
        <Route path="/profiles" element={<ProfilesPage />} />
        <Route path="/profiles/:profileId" element={<ProfileDetailPage />} />
        <Route path="/device-services" element={<DeviceServicesPage />} />
        <Route path="/secrets" element={<SecretsPage />} />
        <Route
          path="/edge-secrets"
          element={<Navigate to="/secrets" replace />}
        />
        <Route
          path="/edge-secret-types"
          element={<Navigate to="/secrets?tab=types" replace />}
        />
        <Route path="/gateway-operations" element={<GatewayOperationsPage />} />
        <Route path="/rule-chains" element={<RuleChainsPage />} />
        <Route path="/business-orchestration" element={<BusinessOrchestrationPage />} />
        <Route path="/forwarders" element={<ForwardersPage />} />
        <Route path="/companion-endpoints" element={<CompanionEndpointsPage />} />
        <Route path="/escalations" element={<EscalationsPage />} />
        <Route path="/rule-chains/g/:groupId" element={<RuleChainEditor />} />
        <Route path="/storage-hooks" element={<StorageHooksPage />} />
        <Route path="/dlq" element={<DLQPage />} />
        <Route path="/commands" element={<CommandCenterPage />} />
        <Route path="/probes" element={<ProbesPage />} />
        <Route path="/alarms" element={<AlarmsPage />} />
        <Route path="/account/security" element={<AccountSecurityPage />} />
        <Route path="/notifications" element={<NotificationsPage />} />
        <Route path="/stats" element={<StatsPage />} />
        <Route path="/help" element={<HelpCenterPage />} />
        <Route path="/help/glossary/:termId" element={<HelpCenterPage />} />
        <Route path="/help/:docId" element={<HelpCenterPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}
