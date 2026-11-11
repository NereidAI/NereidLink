// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// CustomersPage manages customers and device groups: two tabs of
// server-loaded tables with create/edit/delete actions.

import { useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Popconfirm,
  Space,
  Table,
  Tabs,
  Tooltip,
} from "antd";
import {
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import CustomerFormDrawer from "../components/CustomerFormDrawer";
import GroupFormDrawer from "../components/GroupFormDrawer";
import { Link } from "react-router-dom";
import {
  customerDeviceCounts,
  deleteCustomer,
  deleteDeviceGroup,
  groupDeviceCounts,
  listCustomers,
  listDeviceGroups,
  type Customer,
  type DeviceGroup,
} from "../api/ownership";

// CustomersPage renders the customers and groups management page.
export default function CustomersPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const [customerModal, setCustomerModal] = useState(false);
  const [editingCustomer, setEditingCustomer] = useState<Customer | undefined>();
  const [groupModal, setGroupModal] = useState(false);
  const [editingGroup, setGroupGroup] = useState<DeviceGroup | undefined>();
  const [customerKeyword, setCustomerKeyword] = useState("");
  const [groupKeyword, setGroupKeyword] = useState("");
  const [groupCustomerFilter, setGroupCustomerFilter] = useState("");

  const customersQuery = useQuery({ queryKey: ["customers"], queryFn: listCustomers });
  const groupsQuery = useQuery({ queryKey: ["device-groups"], queryFn: listDeviceGroups });
  const customerCountsQuery = useQuery({
    queryKey: ["customer-device-counts"],
    queryFn: customerDeviceCounts,
  });
  const groupCountsQuery = useQuery({
    queryKey: ["group-device-counts"],
    queryFn: groupDeviceCounts,
  });

  // Memoized so the filtered-list memos keep stable dependency identities
  // instead of seeing fresh [] fallbacks on every render.
  const customers = useMemo(
    () => customersQuery.data ?? [],
    [customersQuery.data],
  );
  const groups = useMemo(() => groupsQuery.data ?? [], [groupsQuery.data]);

  const customerName = new Map(customers.map((customer) => [customer.id, customer.name]));

  // filteredCustomers applies client-side keyword search across the
  // customer name, contact and external ID; both lists are unpaged.
  const filteredCustomers = useMemo(() => {
    const needle = customerKeyword.trim().toLowerCase();
    if (!needle) return customers;
    return customers.filter((customer) =>
      [
        customer.name,
        customer.contact_name ?? "",
        customer.contact_email ?? "",
        customer.external_id ?? "",
      ].some((value) => value.toLowerCase().includes(needle)),
    );
  }, [customers, customerKeyword]);

  // filteredGroups applies keyword search over the group name plus an
  // optional customer filter built from the already-loaded customers.
  const filteredGroups = useMemo(() => {
    const needle = groupKeyword.trim().toLowerCase();
    return groups.filter((group) => {
      if (groupCustomerFilter && group.customer_id !== groupCustomerFilter) {
        return false;
      }
      if (!needle) return true;
      return group.name.toLowerCase().includes(needle);
    });
  }, [groups, groupKeyword, groupCustomerFilter]);

  const deleteCustomerMutation = useMutation({
    mutationFn: deleteCustomer,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["customers"] });
      message.success(t("pages.customers.toasts.deleted"));
    },
    onError: () => message.error(t("pages.customers.toasts.deleteFailed")),
  });

  const deleteGroupMutation = useMutation({
    mutationFn: deleteDeviceGroup,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["device-groups"] });
      message.success(t("pages.customers.toasts.deleted"));
    },
    onError: () => message.error(t("pages.customers.toasts.deleteFailed")),
  });

  const customerColumns = [
    {
      title: t("pages.customers.fields.name"),
      dataIndex: "name",
      render: (name: string, customer: Customer) => (
        <Link to={`/devices?customer_id=${customer.id}`}>{name}</Link>
      ),
    },
    {
      title: t("pages.customers.fields.deviceCount"),
      key: "deviceCount",
      width: 110,
      render: (_: unknown, customer: Customer) =>
        customerCountsQuery.data?.[customer.id] ?? 0,
    },
    {
      title: t("pages.customers.fields.contactName"),
      dataIndex: "contact_name",
      render: (value: string) => value || "-",
    },
    {
      title: t("pages.customers.fields.contactEmail"),
      dataIndex: "contact_email",
      render: (value: string) => value || "-",
    },
    {
      title: t("pages.customers.fields.externalId"),
      dataIndex: "external_id",
      render: (value: string) => value || "-",
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 100,
      render: (_: unknown, customer: Customer) => (
        <Space>
          <Tooltip title={t("common.edit")}>
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              onClick={() => {
                setEditingCustomer(customer);
                setCustomerModal(true);
              }}
            />
          </Tooltip>
          <Popconfirm
            title={t("pages.customers.confirm.deleteTitle")}
            okText={t("common.delete")}
            okButtonProps={{ danger: true }}
            onConfirm={() => deleteCustomerMutation.mutate(customer.id)}
          >
            <Tooltip title={t("common.delete")}>
              <Button type="text" size="small" danger icon={<DeleteOutlined />} />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  const groupColumns = [
    {
      title: t("pages.customers.fields.groupName"),
      dataIndex: "name",
      render: (name: string, group: DeviceGroup) => (
        <Link to={`/devices?group_id=${group.id}`}>{name}</Link>
      ),
    },
    {
      title: t("pages.customers.fields.deviceCount"),
      key: "deviceCount",
      width: 110,
      render: (_: unknown, group: DeviceGroup) =>
        groupCountsQuery.data?.[group.id] ?? 0,
    },
    {
      title: t("pages.customers.fields.customer"),
      dataIndex: "customer_id",
      render: (id: string) => (id ? customerName.get(id) ?? id : "-"),
    },
    {
      title: t("pages.customers.fields.parent"),
      dataIndex: "parent_id",
      render: (_: unknown, group: DeviceGroup) => {
        if (!group.parent_id) {
          return "-";
        }
        const parent = groups.find((candidate) => candidate.id === group.parent_id);
        return parent ? parent.name : group.parent_id;
      },
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 100,
      render: (_: unknown, group: DeviceGroup) => (
        <Space>
          <Tooltip title={t("common.edit")}>
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              onClick={() => {
                setGroupGroup(group);
                setGroupModal(true);
              }}
            />
          </Tooltip>
          <Popconfirm
            title={t("pages.customers.confirm.deleteTitle")}
            okText={t("common.delete")}
            okButtonProps={{ danger: true }}
            onConfirm={() => deleteGroupMutation.mutate(group.id)}
          >
            <Tooltip title={t("common.delete")}>
              <Button type="text" size="small" danger icon={<DeleteOutlined />} />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.customers.title")}
        subtitle={t("pages.customers.subtitle")}
        helpId="customers"
      />
      <Card>
        <Tabs
          items={[
            {
              key: "customers",
              label: t("pages.customers.tabs.customers"),
              children: (
                <>
                  <Space style={{ marginBottom: 16 }}>
                    <Button
                      type="primary"
                      icon={<PlusOutlined />}
                      onClick={() => {
                        setEditingCustomer(undefined);
                        setCustomerModal(true);
                      }}
                    >
                      {t("pages.customers.createCustomer")}
                    </Button>
                  </Space>
                  <FilterSearchBar
                    searchValue={customerKeyword}
                    onSearchChange={setCustomerKeyword}
                    searchPlaceholder={t("pages.customers.filters.searchCustomers")}
                  />
                  <Table
                    rowKey="id"
                    loading={customersQuery.isLoading}
                    columns={customerColumns}
                    dataSource={filteredCustomers}
                    pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
                    locale={{ emptyText: t("common.noFilterResults") }}
                  />
                </>
              ),
            },
            {
              key: "groups",
              label: t("pages.customers.tabs.groups"),
              children: (
                <>
                  <Space style={{ marginBottom: 16 }}>
                    <Button
                      type="primary"
                      icon={<PlusOutlined />}
                      onClick={() => {
                        setGroupGroup(undefined);
                        setGroupModal(true);
                      }}
                    >
                      {t("pages.customers.createGroup")}
                    </Button>
                  </Space>
                  <FilterSearchBar
                    searchValue={groupKeyword}
                    onSearchChange={setGroupKeyword}
                    searchPlaceholder={t("pages.customers.filters.searchGroups")}
                    filters={[
                      {
                        key: "customer_id",
                        placeholder: t("pages.customers.filters.allCustomers"),
                        value: groupCustomerFilter,
                        onChange: setGroupCustomerFilter,
                        options: customers.map((customer) => ({
                          value: customer.id,
                          label: customer.name,
                        })),
                      },
                    ]}
                  />
                  <Table
                    rowKey="id"
                    loading={groupsQuery.isLoading}
                    columns={groupColumns}
                    dataSource={filteredGroups}
                    pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
                    locale={{ emptyText: t("common.noFilterResults") }}
                  />
                </>
              ),
            },
          ]}
        />
      </Card>
      <CustomerFormDrawer
        open={customerModal}
        customer={editingCustomer}
        onClose={() => setCustomerModal(false)}
      />
      <GroupFormDrawer
        open={groupModal}
        group={editingGroup}
        onClose={() => setGroupModal(false)}
      />
    </>
  );
}
