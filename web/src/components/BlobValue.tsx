// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// BlobValue renders one telemetry value shaped "blob://<key>". It resolves
// the reference once (short-lived signed URL + content type + size), then
// renders by media type: image thumbnail with full-screen preview, a video
// player modal, a text viewer modal, or a download link for other binaries.

import { useEffect, useMemo, useState } from "react";
import {
  Button,
  Image,
  Modal,
  Space,
  Spin,
  Tooltip,
  Typography,
} from "antd";
import {
  DownloadOutlined,
  FileTextOutlined,
  PlayCircleFilled,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import { signBlob, type SignedBlob } from "../api/blobs";
import { extractError } from "../api/client";

// signedURLTTL matches the backend presign lifetime (15m) with margin.
const SIGNED_URL_TTL_MS = 12 * 60 * 1000;

// textPreviewCap bounds how much of a text object is rendered in the modal.
const TEXT_PREVIEW_CAP = 256 * 1024;

// signedBlobCache shares resolutions across component instances; a signed
// URL is valid for every viewer, and sign calls should not multiply.
const signedBlobCache = new Map<
  string,
  { blob: SignedBlob; expires: number }
>();

// blobKind classifies an object by content type.
type BlobKind = "image" | "video" | "text" | "other";

// classifyKind maps a content type to a renderer family. Unknown and empty
// types become "other" (download only) because the platform never guesses.
function classifyKind(contentType: string): BlobKind {
  const type = contentType.toLowerCase();
  if (type.startsWith("image/")) {
    return "image";
  }
  if (type.startsWith("video/")) {
    return "video";
  }
  if (
    type.startsWith("text/") ||
    type === "application/json" ||
    type === "application/xml" ||
    type === "application/javascript" ||
    type === "application/x-yaml" ||
    type === "application/xhtml+xml"
  ) {
    return "text";
  }
  return "other";
}

interface BlobValueProps {
  // value is the telemetry v_string ("blob://<key>").
  value: string;
}

// BlobValue resolves and renders one blob reference.
export default function BlobValue({ value }: BlobValueProps) {
  const { t } = useTranslation();
  const objectKey = value.slice("blob://".length);

  // Read a still-valid cached resolution during initialization so the
  // render is instant and the effect only handles the asynchronous
  // sign call; synchronously resetting state inside the effect would
  // trigger cascading renders.
  const [blob, setBlob] = useState<SignedBlob | null>(() => {
    const cached = signedBlobCache.get(objectKey);
    return cached && cached.expires > Date.now() ? cached.blob : null;
  });
  const [failed, setFailed] = useState(false);
  const [videoOpen, setVideoOpen] = useState(false);
  const [textOpen, setTextOpen] = useState(false);
  const [textContent, setTextContent] = useState("");
  const [textTruncated, setTextTruncated] = useState(false);
  const [textLoading, setTextLoading] = useState(false);

  // Resolve on mount or when the key changes. A cached resolution was
  // already read in the state initializer, so this effect only performs
  // the asynchronous sign call when needed.
  useEffect(() => {
    if (blob) {
      return;
    }
    let cancelled = false;
    signBlob(objectKey)
      .then((resolved) => {
        if (cancelled) {
          return;
        }
        signedBlobCache.set(objectKey, {
          blob: resolved,
          expires: Date.now() + SIGNED_URL_TTL_MS,
        });
        setBlob(resolved);
        setFailed(false);
      })
      .catch((error) => {
        if (cancelled) {
          return;
        }
        setFailed(extractError(error).status === 404);
      });
    return () => {
      cancelled = true;
    };
  }, [blob, objectKey]);

  const kind = useMemo(
    () => (blob ? classifyKind(blob.contentType) : "other"),
    [blob],
  );

  // loadText fetches the text object lazily the first time its modal opens.
  // S3 without CORS may block the fetch; the modal then shows the fallback
  // message while direct download still works.
  async function loadText() {
    if (!blob || textContent) {
      return;
    }
    setTextLoading(true);
    try {
      const response = await fetch(blob.url);
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
      const buffer = await response.arrayBuffer();
      const bytes = new Uint8Array(buffer);
      const shown = bytes.subarray(0, TEXT_PREVIEW_CAP);
      setTextContent(new TextDecoder().decode(shown));
      setTextTruncated(bytes.length > TEXT_PREVIEW_CAP);
    } catch {
      setTextContent(t("pages.deviceDetail.telemetry.blob.previewFailed"));
      setTextTruncated(false);
    } finally {
      setTextLoading(false);
    }
  }

  if (failed) {
    return (
      <Typography.Text type="secondary">
        {t("pages.deviceDetail.telemetry.blob.notFound")}
      </Typography.Text>
    );
  }
  if (!blob) {
    return <Spin size="small" />;
  }

  // Download is always offered regardless of renderer family.
  const download = (
    <Tooltip title={t("pages.deviceDetail.telemetry.blob.download")}>
      <Button
        type="text"
        size="small"
        icon={<DownloadOutlined />}
        href={blob.url}
        target="_blank"
        rel="noreferrer"
      />
    </Tooltip>
  );

  if (kind === "image") {
    return (
      <Space size={4}>
        <Image
          src={blob.url}
          width={56}
          height={56}
          style={{ objectFit: "cover", borderRadius: 4 }}
        />
        {download}
      </Space>
    );
  }

  if (kind === "video") {
    return (
      <Space size={4}>
        <Button
          type="link"
          size="small"
          icon={<PlayCircleFilled style={{ fontSize: 22 }} />}
          onClick={() => setVideoOpen(true)}
        />
        <Modal
          title={t("pages.deviceDetail.telemetry.blob.videoPreview")}
          open={videoOpen}
          footer={null}
          width={720}
          onCancel={() => setVideoOpen(false)}
          destroyOnClose
        >
          <video
            controls
            autoPlay
            style={{ width: "100%" }}
            src={blob.url}
          />
        </Modal>
        {download}
      </Space>
    );
  }

  if (kind === "text") {
    return (
      <Space size={4}>
        <Button
          type="link"
          size="small"
          icon={<FileTextOutlined style={{ fontSize: 18 }} />}
          onClick={() => {
            setTextOpen(true);
            void loadText();
          }}
        />
        <Modal
          title={t("pages.deviceDetail.telemetry.blob.textPreview")}
          open={textOpen}
          footer={null}
          width={720}
          onCancel={() => setTextOpen(false)}
        >
          <Spin spinning={textLoading}>
            {textTruncated && (
              <Typography.Paragraph type="warning">
                {t("pages.deviceDetail.telemetry.blob.textTruncated", {
                  size: "256 KiB",
                })}
              </Typography.Paragraph>
            )}
            <pre
              style={{
                maxHeight: "60vh",
                overflow: "auto",
                whiteSpace: "pre-wrap",
                wordBreak: "break-word",
              }}
            >
              {textContent}
            </pre>
          </Spin>
        </Modal>
        {download}
      </Space>
    );
  }

  return download;
}
