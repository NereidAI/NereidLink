// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// EditableSection is the read-first card shell used by every section of the
// profile detail page. It shows a compact read-only view by default and
// reveals an editing form (its children) only after "Edit" is pressed, so a
// first-time visitor sees what a profile is instead of a wall of inputs. The
// edit mode is controlled by the parent, which flips it back off once the
// update mutation succeeds. The edit form is mounted on demand and unmounted
// on cancel/save: mounting fresh is what lets antd Form.List/initialValues
// populate reliably (setFieldsValue after mount does not create list rows).
// The owning section passes its form instance so this shell's Save button
// submits it.

import type { ReactNode } from "react";
import { Button, Card, Space } from "antd";
import { EditOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import type { FormInstance } from "antd";

interface EditableSectionProps {
  // title is the card heading.
  title: string;
  // editing controls whether the read view or the form is shown.
  editing: boolean;
  // onEdit enters edit mode.
  onEdit: () => void;
  // onCancel leaves edit mode without saving.
  onCancel: () => void;
  // readView renders the summary shown while not editing.
  readView: ReactNode;
  // form, when supplied, lets the Save button submit the embedded form.
  form?: FormInstance;
  // saving disables the controls during the update mutation.
  saving?: boolean;
  // children is the editing form, shown only in edit mode.
  children?: ReactNode;
}

// EditableSection renders the read/edit card.
export function EditableSection({
  title,
  editing,
  onEdit,
  onCancel,
  readView,
  form,
  saving,
  children,
}: EditableSectionProps) {
  const { t } = useTranslation();

  return (
    <Card
      title={title}
      style={{ marginBottom: 16 }}
      extra={
        !editing ? (
          <Button type="text" icon={<EditOutlined />} onClick={onEdit}>
            {t("common.edit")}
          </Button>
        ) : undefined
      }
    >
      {editing ? (
        <>
          {children}
          <Space style={{ marginTop: 8, display: "flex", justifyContent: "flex-end" }}>
            <Button onClick={onCancel}>{t("common.cancel")}</Button>
            <Button
              type="primary"
              loading={saving}
              onClick={() => form?.submit()}
            >
              {t("common.save")}
            </Button>
          </Space>
        </>
      ) : (
        readView
      )}
    </Card>
  );
}
