// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// AppLayout implements the fixed application chrome required by ADR-0030: a
// collapsible Sider (256/80) with a data-driven menu and version footer, a
// sticky Header (collapse button at the far left, breadcrumb, language
// switch, placeholder user menu), and the padded Content area. There is no
// Footer.
//
// Stage 1 runs without an authenticated user, so the user menu is a static
// "dev mode" placeholder; it is replaced once the identity subsystem lands.

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Outlet } from "react-router-dom";
import {
  Avatar,
  Breadcrumb,
  Button,
  Dropdown,
  Layout as AntLayout,
  Menu,
  Select,
  Space,
  Tooltip,
  Typography,
} from "antd";
import type { MenuProps } from "antd";
import {
  ApiOutlined,
  AppstoreOutlined,
  BarChartOutlined,
  BellOutlined,
  CloudUploadOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  DeploymentUnitOutlined,
  ExportOutlined,
  FolderOutlined,
  FundOutlined,
  HddOutlined,
  KeyOutlined,
  LockOutlined,
  ThunderboltOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  QuestionCircleOutlined,
  RiseOutlined,
  RocketOutlined,
  SafetyCertificateOutlined,
  SafetyOutlined,
  SendOutlined,
  SettingOutlined,
  ShareAltOutlined,
  TeamOutlined,
  UserOutlined,
  WarningOutlined,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import { Link, useLocation, useNavigate } from "react-router-dom";

import i18n from "../i18n";
import HelpDrawer from "../help/HelpDrawer";
import { logout } from "../api/auth";
import { authStore } from "../store/authStore";
import { useWSStore } from "../store/wsStore";

const { Sider, Header, Content } = AntLayout;

const SIDER_WIDTH = 256;
const SIDER_COLLAPSED_WIDTH = 80;
const SIDER_STATE_KEY = "nereidlink.sider.collapsed";

// MenuLeaf is a navigable menu entry whose key is its route path.
interface MenuLeaf {
  key: string;
  icon: ReactNode;
  labelKey: string;
  requiredRole?: "platform" | "tenant"; // visibility filter
}

// MenuGroup groups navigable leaves and itself carries no route.
interface MenuGroup {
  key: string;
  icon: ReactNode;
  labelKey: string;
  children: MenuLeaf[];
  requiredRole?: "platform" | "tenant";
}

type MenuEntry = MenuLeaf | MenuGroup;

// isGroup narrows a MenuEntry to MenuGroup.
const isGroup = (entry: MenuEntry): entry is MenuGroup =>
  "children" in entry;

// roleMatches returns true if the entry is visible for the current user.
const roleMatches = (
  entry: MenuEntry,
  isPlatformAdmin: boolean
): boolean => {
  if (!entry.requiredRole) return true;
  if (entry.requiredRole === "platform") return isPlatformAdmin;
  return !isPlatformAdmin;
};

// filterEntries removes entries that don't match the user's role.
const filterEntries = (
  entries: MenuEntry[],
  isPlatformAdmin: boolean
): MenuEntry[] =>
  entries
    .filter((e) => roleMatches(e, isPlatformAdmin))
    .map((e) => {
      if (isGroup(e)) {
        const filteredChildren = e.children.filter((c) =>
          roleMatches(c, isPlatformAdmin)
        );
        return filteredChildren.length > 0 ? { ...e, children: filteredChildren } : null;
      }
      return e;
    })
    .filter((e): e is MenuEntry => e !== null);

// menuEntries defines the complete sidebar tree. Order within groups is
// meaningful: Overview → Devices & Ownership → Monitoring & Operations →
// Configuration → System (platform-only).
// Role flags control visibility for tenant vs platform users.
const menuEntries: MenuEntry[] = [
  {
    key: "/overview",
    icon: <DashboardOutlined />,
    labelKey: "nav.overview",
  },
  {
    key: "/dashboards",
    icon: <FundOutlined />,
    labelKey: "nav.dashboards",
  },
  {
    key: "grp-devices-ownership",
    icon: <AppstoreOutlined />,
    labelKey: "nav.devicesOwnership",
    children: [
      { key: "/devices", icon: <DatabaseOutlined />, labelKey: "nav.devices" },
      { key: "/customers", icon: <UserOutlined />, labelKey: "nav.customers" },
    ],
  },
  {
    key: "grp-firmware-ota",
    icon: <CloudUploadOutlined />,
    labelKey: "nav.firmwareOta",
    children: [
      { key: "/firmware", icon: <HddOutlined />, labelKey: "nav.firmware" },
      { key: "/ota", icon: <RocketOutlined />, labelKey: "nav.ota" },
    ],
  },
  {
    key: "grp-monitoring-operations",
    icon: <BarChartOutlined />,
    labelKey: "nav.monitoringOperations",
    children: [
      { key: "/commands", icon: <SendOutlined />, labelKey: "nav.commands" },
      { key: "/probes", icon: <ApiOutlined />, labelKey: "nav.probes" },
      { key: "/alarms", icon: <WarningOutlined />, labelKey: "nav.alarms" },
      { key: "/notifications", icon: <BellOutlined />, labelKey: "nav.notifications" },
      { key: "/stats", icon: <BarChartOutlined />, labelKey: "nav.stats" },
    ],
  },
  {
    key: "grp-configuration",
    icon: <SettingOutlined />,
    labelKey: "nav.configuration",
    children: [
      { key: "/profiles", icon: <SafetyOutlined />, labelKey: "nav.profiles" },
      { key: "/device-services", icon: <ApiOutlined />, labelKey: "nav.deviceServices" },
      { key: "/secrets", icon: <LockOutlined />, labelKey: "nav.secrets" },
      { key: "/gateway-operations", icon: <ThunderboltOutlined />, labelKey: "nav.operations" },
      { key: "/rule-chains", icon: <ShareAltOutlined />, labelKey: "nav.ruleChains" },
      { key: "/business-orchestration", icon: <DeploymentUnitOutlined />, labelKey: "nav.businessOrchestration" },
      { key: "/forwarders", icon: <ExportOutlined />, labelKey: "nav.forwarders" },
      { key: "/companion-endpoints", icon: <AppstoreOutlined />, labelKey: "nav.companionEndpoints" },
      { key: "/escalations", icon: <RiseOutlined />, labelKey: "nav.escalations" },
      { key: "/storage-hooks", icon: <ApiOutlined />, labelKey: "nav.storageHooks" },
      // Same route as the platform-group entry; role-exclusive so a
      // platform admin never sees two /system/users menu keys.
      {
        key: "/system/users",
        icon: <TeamOutlined />,
        labelKey: "nav.systemUsers",
        requiredRole: "tenant",
      },
      { key: "/dlq", icon: <WarningOutlined />, labelKey: "nav.dlq" },
    ],
  },
  // Platform-only group: always present in the array but filtered out for
  // tenant-scoped accounts by requiredRole: "platform".
  {
    key: "grp-system",
    icon: <SafetyCertificateOutlined />,
    labelKey: "nav.system",
    requiredRole: "platform",
    children: [
      { key: "/system/tenants", icon: <UserOutlined />, labelKey: "nav.systemTenants" },
      { key: "/system/users", icon: <TeamOutlined />, labelKey: "nav.systemUsers" },
      { key: "/system/audit", icon: <SafetyCertificateOutlined />, labelKey: "nav.systemAudit" },
      { key: "/system/cas", icon: <SafetyCertificateOutlined />, labelKey: "nav.systemCAs" },
      { key: "/system/license", icon: <KeyOutlined />, labelKey: "nav.systemLicense" },
      { key: "/system/ops-orders", icon: <FolderOutlined />, labelKey: "nav.opsOrders" },
    ],
  },
];

// titleByPath maps a route segment to its translation key for breadcrumbs.
const titleByPath: Record<string, string> = {
  account: "userMenu.account",
  security: "userMenu.accountSecurity",
  overview: "nav.overview",
  dashboards: "nav.dashboards",
  system: "nav.system",
  tenants: "nav.systemTenants",
  users: "nav.systemUsers",
  "ops-orders": "nav.opsOrders",
  license: "nav.systemLicense",
  devices: "nav.devices",
  customers: "nav.customers",
  firmware: "nav.firmware",
  ota: "nav.ota",
  commands: "nav.commands",
  profiles: "nav.profiles",
  "device-services": "nav.deviceServices",
  secrets: "nav.secrets",
  "gateway-operations": "nav.operations",
  "rule-chains": "nav.ruleChains",
  "business-orchestration": "nav.businessOrchestration",
  "storage-hooks": "nav.storageHooks",
  dlq: "nav.dlq",
  alarms: "nav.alarms",
  notifications: "nav.notifications",
  stats: "nav.stats",
  help: "nav.help",
  audit: "nav.systemAudit",
  cas: "nav.systemCAs",
};

// AppLayout renders the application shell around the routed content.
export default function AppLayout() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const user = authStore((state) => state.user);
  const isPlatformAdmin = user?.is_platform_admin ?? false;

  const [signingOut, setSigningOut] = useState(false);
  const [collapsed, setCollapsed] = useState(() => {
    if (typeof window !== "undefined") {
      const saved = localStorage.getItem(SIDER_STATE_KEY);
      return saved === "true";
    }
    return false;
  });
  const [openKeys, setOpenKeys] = useState<string[]>([]);

  // Persist collapsed state.
  useEffect(() => {
    localStorage.setItem(SIDER_STATE_KEY, String(collapsed));
  }, [collapsed]);

  // Filter menu entries by role.
  const entries = useMemo(
    () => filterEntries(menuEntries, isPlatformAdmin),
    [isPlatformAdmin]
  );

  // renderMenu maps the typed tree into AntD menu item props.
  const renderMenu = useMemo(() => {
    const map = (list: MenuEntry[]): NonNullable<MenuProps["items"]> =>
      list.map((entry) => {
        if (isGroup(entry)) {
          return {
            key: entry.key,
            icon: entry.icon,
            label: t(entry.labelKey),
            children: map(entry.children),
          };
        }
        return {
          key: entry.key,
          icon: entry.icon,
          label: t(entry.labelKey),
        };
      });
    return map(entries);
  }, [entries, t]);

  // longestPrefix finds the most specific leaf route for menu highlight and
  // returns its parent group so the containing submenu stays open.
  const active = useMemo(() => {
    let leaf = "";
    let group = "";
    const walk = (list: MenuEntry[], parent = "") => {
      for (const entry of list) {
        if (isGroup(entry)) {
          walk(entry.children, entry.key);
        } else if (
          (location.pathname === entry.key ||
            location.pathname.startsWith(`${entry.key}/`)) &&
          entry.key.length > leaf.length
        ) {
          leaf = entry.key;
          group = parent;
        }
      }
    };
    walk(entries);
    return { leaf, group };
  }, [entries, location.pathname]);

  // Navigating inside a group auto-expands that group once. This is the
  // React-recommended "adjust state when a derived input changes" pattern:
  // comparing against the previous group during render avoids an effect
  // whose only job was to call setState after paint.
  const [prevActiveGroup, setPrevActiveGroup] = useState("");
  if (active.group !== prevActiveGroup) {
    setPrevActiveGroup(active.group);
    if (active.group && !openKeys.includes(active.group)) {
      setOpenKeys((previous) => [...previous, active.group]);
    }
  }

  // The single WebSocket connection lives for the authenticated
  // session; handshake bearer authentication is enforced with the RBAC
  // stage.
  const connectWS = useWSStore((state) => state.connect);
  useEffect(() => {
    connectWS();
  }, [connectWS]);

  // breadcrumbItems translate known route segments; dynamic UUID segments
  // are rendered verbatim and truncated.
  const breadcrumbItems = location.pathname
    .split("/")
    .filter(Boolean)
    .map((segment, index, array) => {
      const target = `/${array.slice(0, index + 1).join("/")}`;
      const labelKey = titleByPath[segment];
      const label = labelKey
        ? t(labelKey)
        : segment.length > 12
          ? `${segment.slice(0, 12)}...`
          : segment;
      const isLast = index === array.length - 1;
      return {
        title: isLast ? label : <Link to={target}>{label}</Link>,
      };
    });

  const languageOptions = [
    { value: "en", label: "English" },
    { value: "zh", label: "中文" },
  ];

  // User menu: identity/profile of the signed-in user and explicit
  // sign-out, which revokes the server session before clearing local
  // credentials.
  const onSignOut = async () => {
    setSigningOut(true);
    try {
      await logout();
    } finally {
      authStore.getState().clearSession();
      setSigningOut(false);
      navigate("/login", { replace: true });
    }
  };

  const userMenuItems: MenuProps["items"] = [
    {
      key: "identity",
      label: user?.email ?? "",
      disabled: true,
    },
    { type: "divider" },
    {
      key: "/account/security",
      label: t("userMenu.accountSecurity"),
    },
    {
      key: "signout",
      label: signingOut ? t("userMenu.signingOut") : t("userMenu.signOut"),
    },
  ];

  return (
    <AntLayout style={{ minHeight: "100vh" }}>
      <Sider
        trigger={null}
        collapsible
        collapsed={collapsed}
        width={SIDER_WIDTH}
        collapsedWidth={SIDER_COLLAPSED_WIDTH}
        theme="light"
        className="app-layout-sider"
        style={{
          borderRight: "1px solid #f0f0f0",
          height: "100vh",
          position: "fixed",
          inset: 0,
          right: "auto",
          zIndex: 100,
          display: "flex",
          flexDirection: "column",
          overflow: "hidden",
          transition: "width 0.2s, background 0.2s",
        }}
      >
        {/* Brand header */}
        <div
          style={{
            height: 64,
            display: "flex",
            alignItems: "center",
            justifyContent: collapsed ? "center" : "flex-start",
            padding: "0 16px",
            borderBottom: "1px solid #f0f0f0",
          }}
        >
          <span style={{ fontSize: 18, fontWeight: 600, color: "#1677ff" }}>
            {collapsed ? "NL" : "NereidLink"}
          </span>
        </div>

        {/* Scrollable menu region between brand header and version footer */}
        <div style={{ flex: 1, overflowY: "auto", minHeight: 0 }}>
          <Menu
            mode="inline"
            theme="light"
            selectedKeys={active.leaf ? [active.leaf] : []}
            onClick={({ key }) => {
              if (!key.startsWith("grp-")) navigate(key);
            }}
            {...(!collapsed
              ? { openKeys, onOpenChange: setOpenKeys }
              : { inlineCollapsed: true })}
            style={{ borderRight: "none", marginTop: 8 }}
            items={renderMenu}
          />
        </div>

        {/* Footer with version */}
        <div
          style={{
            flex: "none",
            padding: "12px 16px",
            borderTop: "1px solid #f0f0f0",
            background: "#fff",
            fontSize: 12,
            textAlign: collapsed ? "center" : "left",
          }}
        >
          {!collapsed && (
            <Tooltip title="Version">
              <Typography.Text type="secondary">v</Typography.Text>
            </Tooltip>
          )}
          <Tooltip title={import.meta.env.VITE_APP_VERSION ?? "dev"}>
            <Typography.Text type="secondary">
              {import.meta.env.VITE_APP_VERSION ?? "dev"}
            </Typography.Text>
          </Tooltip>
        </div>
      </Sider>

      <AntLayout
        style={{
          marginLeft: collapsed ? SIDER_COLLAPSED_WIDTH : SIDER_WIDTH,
          transition: "margin-left 0.2s",
        }}
      >
        <Header
          style={{
            padding: "0 24px",
            height: 64,
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            background: "#fff",
            borderBottom: "1px solid #f0f0f0",
            position: "sticky",
            top: 0,
            zIndex: 50,
          }}
        >
          <Space size="middle">
            <Button
              type="text"
              icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
              onClick={() => setCollapsed(!collapsed)}
              aria-label={collapsed ? t("nav.expand") : t("nav.collapse")}
              style={{ fontSize: 16 }}
            />
            <Breadcrumb items={breadcrumbItems} />
          </Space>
          <Space size="middle">
            <Tooltip title={t("nav.help")}>
              <Button
                type="text"
                icon={<QuestionCircleOutlined />}
                onClick={() => navigate("/help")}
              />
            </Tooltip>
            <Select
              value={i18n.language?.startsWith("zh") ? "zh" : "en"}
              onChange={(value) => void i18n.changeLanguage(value)}
              options={languageOptions}
              size="small"
              style={{ width: 100 }}
            />
            <Dropdown
              menu={{
                items: userMenuItems,
                onClick: ({ key }) => {
                  if (key === "signout") {
                    void onSignOut();
                  } else if (key === "/account/security") {
                    navigate(key);
                  }
                },
              }}
              placement="bottomRight"
            >
              <Space style={{ cursor: "pointer" }}>
                <Avatar size="small" icon={<UserOutlined />} />
                <Typography.Text>{user?.display_name ?? ""}</Typography.Text>
              </Space>
            </Dropdown>
          </Space>
        </Header>
        <Content style={{ padding: 24, background: "#f5f5f5" }}>
          <Outlet />
        </Content>
        <HelpDrawer />
      </AntLayout>
    </AntLayout>
  );
}