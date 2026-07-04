import { useState } from "react";
import type { DeployStatus } from "./api";

export function StatusBadge({ status }: { status: DeployStatus }) {
  return <span className={`badge ${status}`}>{status}</span>;
}

export function timeAgo(iso?: string): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  const secs = Math.floor((Date.now() - then) / 1000);
  if (secs < 0) return "just now";
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  return `${days}d ago`;
}

export function tagOf(image?: string): string {
  if (!image) return "—";
  const at = image.indexOf("@");
  const base = at >= 0 ? image.slice(0, at) : image;
  const slash = base.lastIndexOf("/");
  const colon = base.indexOf(":", slash);
  return colon >= 0 ? base.slice(colon + 1) : "latest";
}

export function CopyField({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="copy-field">
      <span>{value}</span>
      <button
        className="btn sm"
        onClick={() => {
          navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1200);
        }}
      >
        {copied ? "✓" : "Copy"}
      </button>
    </div>
  );
}

export function ErrorBox({ error }: { error: string | null }) {
  if (!error) return null;
  return <div className="error-box">{error}</div>;
}

// downloadText triggers a browser download of a text file (used for YAML export).
export function downloadText(filename: string, text: string, type = "application/x-yaml") {
  const blob = new Blob([text], { type });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
