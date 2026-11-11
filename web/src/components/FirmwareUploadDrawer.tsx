// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// FirmwareUploadDrawer collects one firmware artifact and its metadata and
// streams it to the platform repository. Metadata form fields are submitted
// before the file part, which the server requires to stream the upload.

import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  Select,
  Switch,
  Upload,
  type UploadFile as AntUploadFile,
} from "antd";
import { InboxOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { uploadFirmware } from "../api/firmware";
import { listProfiles } from "../api/devices";
import { extractError } from "../api/client";

interface FirmwareUploadDrawerProps {
  open: boolean;
  onClose: () => void;
}

interface FormValues {
  name: string;
  version: string;
  release_notes?: string;
  profile_id?: string;
  is_stable: boolean;
  file: AntUploadFile[];
}

// FirmwareUploadDrawer renders the upload form.
export default function FirmwareUploadDrawer({
  open,
  onClose,
}: FirmwareUploadDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<FormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const profilesQuery = useQuery({
    queryKey: ["device-profiles"],
    queryFn: listProfiles,
    enabled: open,
  });

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      const upload = values.file[0]?.originFileObj;
      if (!upload) {
        throw new Error("missing file");
      }
      return uploadFirmware({
        file: upload,
        name: values.name.trim(),
        version: values.version.trim(),
        release_notes: values.release_notes?.trim() || undefined,
        filename: upload.name,
        profile_id: values.profile_id || undefined,
        content_type: upload.type || "application/octet-stream",
        is_stable: values.is_stable,
      });
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["firmware"] });
      message.success(t("pages.firmware.toasts.uploaded"));
      onClose();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  return (
    <Drawer
      open={open}
      title={t("pages.firmware.modal.title")}
      onClose={onClose}
      footer={null}
      width={720}
      destroyOnHidden
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        initialValues={{ is_stable: false }}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.Item
          label={t("pages.firmware.modal.name")}
          name="name"
          rules={[
            { required: true, message: t("pages.firmware.modal.nameRequired") },
          ]}
        >
          <Input placeholder={t("pages.firmware.modal.namePlaceholder")} />
        </Form.Item>

        <Form.Item
          label={t("pages.firmware.modal.version")}
          name="version"
          rules={[
            { required: true, message: t("pages.firmware.modal.versionRequired") },
          ]}
        >
          <Input placeholder={t("pages.firmware.modal.versionPlaceholder")} />
        </Form.Item>

        <Form.Item
          label={t("pages.firmware.modal.releaseNotes")}
          name="release_notes"
        >
          <Input.TextArea
            rows={3}
            showCount
            maxLength={10000}
            placeholder={t("pages.firmware.modal.releaseNotesPlaceholder")}
          />
        </Form.Item>

        <Form.Item
          label={t("pages.firmware.modal.profile")}
          name="profile_id"
          extra={t("pages.firmware.modal.profileHint")}
        >
          <Select
            allowClear
            showSearch
            optionFilterProp="label"
            placeholder={t("pages.firmware.profileGeneric")}
            loading={profilesQuery.isLoading}
            options={(profilesQuery.data ?? []).map((profile) => ({
              value: profile.id,
              label: profile.name,
            }))}
          />
        </Form.Item>

        <Form.Item
          label={t("pages.firmware.modal.file")}
          name="file"
          valuePropName="fileList"
          getValueFromEvent={(event) =>
            Array.isArray(event) ? event.slice(-1) : event?.fileList?.slice(-1)
          }
          rules={[
            { required: true, message: t("pages.firmware.modal.fileRequired") },
          ]}
        >
          <Upload.Dragger beforeUpload={() => false} maxCount={1}>
            <p className="ant-upload-drag-icon">
              <InboxOutlined />
            </p>
            <p className="ant-upload-text">
              {t("pages.firmware.modal.fileDrop")}
            </p>
            <p className="ant-upload-hint">{t("pages.firmware.modal.fileHint")}</p>
          </Upload.Dragger>
        </Form.Item>

        <Form.Item
          label={t("pages.firmware.modal.stable")}
          name="is_stable"
          valuePropName="checked"
        >
          <Switch />
        </Form.Item>

        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Button onClick={onClose} style={{ marginRight: 8 }}>
            {t("common.cancel")}
          </Button>
          <Button type="primary" htmlType="submit" loading={mutation.isPending}>
            {t("pages.firmware.upload")}
          </Button>
        </Form.Item>
      </Form>
    </Drawer>
  );
}
