// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// StepUpCard renders the reusable operation-time authentication flow:
// start a one-time TOTP step-up challenge for one action, display the
// signed payload as a QR code, verify the current authenticator code, and
// notify the parent with the verified challenge id. Dangerous gateway
// operations and forced secret deletion both use this component.

import { Alert, Button, Card, Form, Input, Space } from "antd";
import { useEffect, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { QRCodeCanvas } from "qrcode.react";

import {
  completeStepUp,
  startStepUp,
  type StepUpChallenge,
} from "../api/stepUp";
import { extractError } from "../api/client";

// StepUpLabels are the caller-localized texts for one card instance.
export interface StepUpLabels {
  // start labels the button that creates the challenge.
  start: string;
  // codePlaceholder labels the TOTP code input.
  codePlaceholder: string;
  // verify labels the code-check button.
  verify: string;
  // verified is shown after successful verification.
  verified: string;
}

interface StepUpCardProps {
  // action is the exact action key the challenge binds to.
  action: string;
  // details optionally carries non-sensitive context (gateway/secret ids).
  details?: Record<string, unknown>;
  // labels provide localized texts.
  labels: StepUpLabels;
  // autoStart creates the challenge on mount instead of waiting for a
  // button press.
  autoStart?: boolean;
  // onVerified delivers the verified challenge id to the parent.
  onVerified?: (challengeId: string) => void;
}

// StepUpCard manages challenge start, TOTP verification and the verified
// state for a single action.
export default function StepUpCard({
  action,
  details,
  labels,
  autoStart = false,
  onVerified,
}: StepUpCardProps) {
  const [challenge, setChallenge] = useState<StepUpChallenge | null>(null);
  const [verified, setVerified] = useState(false);
  const [error, setError] = useState("");
  const [codeForm] = Form.useForm();

  const startMutation = useMutation({
    mutationFn: () => startStepUp(action, details),
    onSuccess: (result) => {
      setChallenge(result);
      setError("");
    },
    onError: (err) => setError(extractError(err).message),
  });

  const verifyMutation = useMutation({
    mutationFn: async () => {
      const values = await codeForm.validateFields();
      return completeStepUp(challenge!.id, values.code as string);
    },
    onSuccess: () => {
      setVerified(true);
      setError("");
      if (challenge) {
        onVerified?.(challenge.id);
      }
    },
    onError: (err) => setError(extractError(err).message),
  });

  // Start once on mount when autoStart is set (e.g. a dangerous-operation
  // modal whose QR must match the visible target immediately).
  useEffect(() => {
    if (autoStart) {
      startMutation.mutate();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (!challenge && !startMutation.isPending) {
    return (
      <Space direction="vertical" style={{ width: "100%" }}>
        {error && (
          <Alert
            type="error"
            showIcon
            message={error}
            closable
            onClose={() => setError("")}
          />
        )}
        <Button onClick={() => startMutation.mutate()}>{labels.start}</Button>
      </Space>
    );
  }

  if (verified) {
    return <Alert type="success" showIcon message={labels.verified} />;
  }

  return (
    <Card size="small">
      <Space direction="vertical" align="center" style={{ width: "100%" }}>
        {error && (
          <Alert
            type="error"
            showIcon
            style={{ width: "100%" }}
            message={error}
            closable
            onClose={() => setError("")}
          />
        )}
        {challenge?.signed_payload && (
          <QRCodeCanvas value={challenge.signed_payload} size={140} />
        )}
        <Form form={codeForm} layout="inline">
          <Form.Item name="code" rules={[{ required: true }]}>
            <Input
              placeholder={labels.codePlaceholder}
              autoComplete="one-time-code"
            />
          </Form.Item>
          <Button
            type="primary"
            loading={verifyMutation.isPending}
            onClick={() => verifyMutation.mutate()}
          >
            {labels.verify}
          </Button>
        </Form>
      </Space>
    </Card>
  );
}
