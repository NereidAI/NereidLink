// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// NodeConfigDrawer edits one selected node. It renders type-specific
// vertical form fields and writes values back into the node data. Node
// name editing is shared across types.

import {
  Button,
  Drawer,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
} from "antd";
import { DeleteOutlined, MinusCircleOutlined, PlusOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { RuleNodeType } from "./RuleNode";
import type { NodeConfig } from "./nodeCatalog";
import { listEscalationChains } from "../../api/escalations";
import { listForwarders } from "../../api/forwarders";

interface NodeConfigDrawerProps {
  // open reports drawer visibility.
  open: boolean;

  // node is the edited node, absent when closed.
  node?: RuleNodeType;

  // onChange persists the edited name/config back to the canvas.
  onChange: (id: string, label: string, config: NodeConfig) => void;

  // onDelete removes the node.
  onDelete: (id: string) => void;

  // onClose hides the drawer.
  onClose: () => void;
}

// NodeConfigDrawer renders the config drawer body.
export default function NodeConfigDrawer({
  open,
  node,
  onChange,
  onDelete,
  onClose,
}: NodeConfigDrawerProps) {
  const { t } = useTranslation();

  if (!node) {
    return <Drawer open={false} />;
  }

  const data = node.data;
  const config = data.config;

  // severityOptions are the alarm severity values.
  const severityOptions = [
    { value: "CRITICAL", label: t("status.critical") },
    { value: "MAJOR", label: t("status.major") },
    { value: "MINOR", label: t("status.minor") },
    { value: "WARNING", label: t("status.warning") },
  ];

  // scopeOptions are the save-attributes scope values.
  const scopeOptions = [
    { value: "CLIENT", label: t("enums.scopeClient") },
    { value: "SERVER_SCOPE", label: t("enums.scopeServer") },
    { value: "SHARED_SCOPE", label: t("enums.scopeShared") },
  ];

  // patch writes one config update through to the canvas, keeping the
  // current name.
  const patch = (next: NodeConfig) => onChange(node.id, data.label, next);

  return (
    <Drawer
      open={open}
      title={t("rule.configTitle")}
      width={480}
      onClose={onClose}
      extra={
        <Button danger icon={<DeleteOutlined />} onClick={() => onDelete(node.id)}>
          {t("rule.controls.delete")}
        </Button>
      }
      destroyOnHidden
    >
      <Form layout="vertical">
        <Form.Item label={t("rule.fields.nodeName")}>
          <Input
            value={data.label}
            onChange={(event) =>
              onChange(node.id, event.target.value, config)
            }
          />
        </Form.Item>

        {node.data.nodeType === "script-filter" && (
          <Form.Item label={t("rule.fields.conditionScript")}>
            <Input.TextArea
              rows={4}
              value={config.script}
              onChange={(event) =>
                patch({ ...config, script: event.target.value })
              }
            />
          </Form.Item>
        )}

        {node.data.nodeType === "switch" && (
          <>
            <Form.Item label={t("rule.fields.cases")}>
              <Space direction="vertical" style={{ width: "100%" }}>
                {(config.cases ?? []).map((entry, index) => (
                  <Space key={index} align="start" style={{ width: "100%" }}>
                    <Input
                      addonBefore={t("rule.fields.label")}
                      value={entry.label}
                      style={{ width: 150 }}
                      onChange={(event) => {
                        const cases = [...(config.cases ?? [])];
                        cases[index] = { ...cases[index], label: event.target.value };
                        patch({ ...config, cases });
                      }}
                    />
                    <Input
                      addonBefore={t("rule.fields.if")}
                      value={entry.script}
                      style={{ width: 220 }}
                      onChange={(event) => {
                        const cases = [...(config.cases ?? [])];
                        cases[index] = { ...cases[index], script: event.target.value };
                        patch({ ...config, cases });
                      }}
                    />
                    <MinusCircleOutlined
                      onClick={() =>
                        patch({
                          ...config,
                          cases: (config.cases ?? []).filter((_, i) => i !== index),
                        })
                      }
                    />
                  </Space>
                ))}
                <Button
                  type="dashed"
                  block
                  icon={<PlusOutlined />}
                  onClick={() =>
                    patch({
                      ...config,
                      cases: [...(config.cases ?? []), { label: "New", script: "return false" }],
                    })
                  }
                >
                  {t("rule.fields.addCase")}
                </Button>
              </Space>
            </Form.Item>
            <Form.Item label={t("rule.fields.defaultRelation")}>
              <Switch
                checked={!!config.has_default}
                onChange={(checked) => patch({ ...config, has_default: checked })}
              />
            </Form.Item>
          </>
        )}

        {node.data.nodeType === "transform" && (
          <Form.Item label={t("rule.fields.transformScript")}>
            <Input.TextArea
              rows={5}
              value={config.script}
              onChange={(event) =>
                patch({ ...config, script: event.target.value })
              }
            />
          </Form.Item>
        )}

        {node.data.nodeType === "save-attributes" && (
          <>
            <Form.Item label={t("rule.fields.scope")}>
              <Select
                value={config.scope}
                options={scopeOptions}
                style={{ width: 200 }}
                onChange={(scope) => patch({ ...config, scope })}
              />
            </Form.Item>
            <Form.Item label={t("rule.fields.attributesScript")}>
              <Input.TextArea
                rows={4}
                value={config.script}
                onChange={(event) =>
                  patch({ ...config, script: event.target.value })
                }
              />
            </Form.Item>
          </>
        )}

        {node.data.nodeType === "create-alarm" && (
          <>
            <Space wrap>
              <Form.Item label={t("rule.fields.alarmType")}>
                <Input
                  value={config.alarm_type}
                  onChange={(event) =>
                    patch({ ...config, alarm_type: event.target.value })
                  }
                />
              </Form.Item>
              <Form.Item label={t("rule.fields.severity")}>
                <Select
                  value={config.severity}
                  options={severityOptions}
                  onChange={(severity) => patch({ ...config, severity })}
                />
              </Form.Item>
            </Space>
            <Form.Item label={t("rule.fields.dedupeKeyScript")}>
              <Input.TextArea
                rows={2}
                value={config.dedupe_key_script}
                onChange={(event) =>
                  patch({ ...config, dedupe_key_script: event.target.value })
                }
              />
            </Form.Item>
            <Form.Item label={t("rule.fields.detailsScript")}>
              <Input.TextArea
                rows={2}
                value={config.details_script}
                onChange={(event) =>
                  patch({ ...config, details_script: event.target.value })
                }
              />
            </Form.Item>
            <Form.Item label={t("rule.fields.propagate")}>
              <Switch
                checked={!!config.propagate}
                onChange={(checked) => patch({ ...config, propagate: checked })}
              />
            </Form.Item>
            <Form.Item label={t("rule.fields.escalationChain")}>
              <EscalationChainSelect
                value={config.escalation_chain_id}
                onChange={(chainID) => patch({ ...config, escalation_chain_id: chainID })}
              />
            </Form.Item>
          </>
        )}

        {node.data.nodeType === "clear-alarm" && (
          <>
            <Form.Item label={t("rule.fields.alarmType")}>
              <Input
                value={config.alarm_type}
                onChange={(event) =>
                  patch({ ...config, alarm_type: event.target.value })
                }
              />
            </Form.Item>
            <Form.Item label={t("rule.fields.dedupeKeyScript")}>
              <Input.TextArea
                rows={2}
                value={config.dedupe_key_script}
                onChange={(event) =>
                  patch({ ...config, dedupe_key_script: event.target.value })
                }
              />
            </Form.Item>
          </>
        )}

        {node.data.nodeType === "send-command" && (
          <>
            <Space wrap>
              <Form.Item label={t("rule.fields.commandName")}>
                <Input
                  value={config.command_name}
                  onChange={(event) =>
                    patch({ ...config, command_name: event.target.value })
                  }
                />
              </Form.Item>
              <Form.Item label={t("rule.fields.timeout")}>
                <InputNumber
                  value={config.timeout_ms}
                  min={0}
                  onChange={(value) =>
                    patch({ ...config, timeout_ms: value ?? 0 })
                  }
                />
              </Form.Item>
            </Space>
            <Form.Item label={t("rule.fields.paramsScript")}>
              <Input.TextArea
                rows={3}
                value={config.params_script}
                onChange={(event) =>
                  patch({ ...config, params_script: event.target.value })
                }
              />
            </Form.Item>
          </>
        )}

        {node.data.nodeType === "forward" && (
          <Form.Item label={t("rule.fields.forwarder")}>
            <ForwarderSelect
              value={config.forwarder_id}
              onChange={(forwarderID) =>
                patch({ ...config, forwarder_id: forwarderID })
              }
            />
          </Form.Item>
        )}

        {node.data.nodeType === "log" && (
          <Form.Item label={t("rule.fields.logMessage")}>
            <Input
              value={config.message}
              onChange={(event) =>
                patch({ ...config, message: event.target.value })
              }
            />
          </Form.Item>
        )}

        {node.data.nodeType === "delimiter" && (
          <Form.Item label={t("rule.fields.arrayField")}>
            <Input
              value={config.field}
              onChange={(event) =>
                patch({ ...config, field: event.target.value })
              }
            />
          </Form.Item>
        )}
      </Form>
    </Drawer>
  );
}

interface EscalationChainSelectProps {
  // value is the selected chain id, or empty for none.
  value?: string;

  // onChange reports the new selection; an empty value clears the binding.
  onChange: (chainID: string) => void;
}

// EscalationChainSelect lists the tenant chains with an explicit "none"
// option. Chains are fetched on demand and shared by the query cache.
function EscalationChainSelect({ value, onChange }: EscalationChainSelectProps) {
  const { t } = useTranslation();
  const { data } = useQuery({
    queryKey: ["escalation-chains"],
    queryFn: listEscalationChains,
  });
  return (
    <Select
      allowClear
      style={{ width: 280 }}
      placeholder={t("rule.fields.escalationChainNone")}
      value={value || undefined}
      options={(data ?? []).map((chain) => ({
        value: chain.chain_id,
        label: chain.name,
      }))}
      onChange={(selected) => onChange(selected ?? "")}
    />
  );
}

interface ForwarderSelectProps {
  // value is the selected forwarder id.
  value?: string;

  // onChange reports the new selection; empty clears it.
  onChange: (forwarderID: string) => void;
}

// ForwarderSelect lists the tenant data forwarders. It is fetched on demand
// and shared through the query cache.
function ForwarderSelect({ value, onChange }: ForwarderSelectProps) {
  const { t } = useTranslation();
  const { data } = useQuery({
    queryKey: ["forwarders"],
    queryFn: listForwarders,
  });
  return (
    <Select
      showSearch
      optionFilterProp="label"
      style={{ width: 280 }}
      placeholder={t("rule.fields.forwarderNone")}
      value={value || undefined}
      options={(data ?? []).map((forwarder) => ({
        value: forwarder.id,
        label: forwarder.name,
      }))}
      onChange={(selected) => onChange(selected ?? "")}
    />
  );
}
