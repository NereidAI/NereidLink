// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProfileDeliveryStateTag renders one profile-delivery state as a colored
// AntD tag, translating the platform/edge vocabulary and surfacing the
// edge-reported reason (FAILED/REJECTED) on hover. The color vocabulary lives
// in utils/profileDeliveryState so every screen renders a state identically.

import { Tag, Tooltip } from "antd";
import { useTranslation } from "react-i18next";

import { ProfileDeliveryStateColor } from "../../utils/profileDeliveryState";

interface ProfileDeliveryStateTagProps {
  // state is one of the profilepush.State* constants.
  state: string;
  // reason, when present, is the edge-reported failure explanation.
  reason?: string;
}

// stateKeyPrefix is the i18n namespace holding the state labels; both the
// profile distribution card and the gateway tab share it.
const stateKeyPrefix = "pages.deviceDetail.profileDelivery.states";

// ProfileDeliveryStateTag renders the state chip.
export default function ProfileDeliveryStateTag({
  state,
  reason,
}: ProfileDeliveryStateTagProps) {
  const { t } = useTranslation();
  const tag = (
    <Tag color={ProfileDeliveryStateColor[state] ?? "default"}>
      {t(`${stateKeyPrefix}.${state}`, { defaultValue: state })}
    </Tag>
  );
  return reason ? <Tooltip title={reason}>{tag}</Tooltip> : tag;
}
