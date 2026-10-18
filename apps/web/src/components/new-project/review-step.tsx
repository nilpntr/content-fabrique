import type * as React from "react";
import { DeviceFrame } from "@/components/device-frame";
import { Button } from "@/components/ui/button";
import { formatDuration } from "./duration";
import { totalDurationMs } from "./footage-step";
import { formatInfo } from "./formats";
import type { Asset, AssetId, Format } from "./types";

export type StepId = "format" | "footage" | "brief";

/** A summary of the three choices, each with a way back to change it. */
export function ReviewStep({
  assets,
  format,
  assetIds,
  brief,
  onEdit,
  disabled = false,
  costNote,
  title = "",
  vibe,
  onMediaError,
}: {
  assets: readonly Asset[];
  format: Format;
  assetIds: readonly AssetId[];
  brief: string;
  onEdit: (step: StepId) => void;
  /** Locks the Edit buttons, e.g. while the project is being created. */
  disabled?: boolean;
  /** What making the video uses from the plan, in plain words (never dollars). */
  costNote?: string;
  /** The project's name, when the creator gave one. */
  title?: string;
  /** The vibe of the video in words ("Lively"). */
  vibe?: string;
  /** A poster would not load: the caller can fetch the list again for fresh URLs. */
  onMediaError?: () => void;
}) {
  const info = formatInfo(format);
  const byId = new Map(assets.map((asset) => [asset.id, asset]));
  const picked = assetIds.flatMap((id) => byId.get(id) ?? []);
  const text = brief.trim();

  return (
    <dl className="divide-y divide-hairline rounded-2xl bg-surface-raised shadow-soft ring-1 ring-border">
      <Row disabled={disabled} term="Format" onEdit={() => onEdit("format")} editLabel="Edit format">
        <div className="flex items-center gap-4">
          <DeviceFrame kind={info.device} postRatio={info.postRatio} size="sm" active />
          <div>
            <p className="text-lg font-semibold tracking-[-0.02em] tabular-nums">
              {info.id} <span className="text-[13px] font-medium tracking-normal text-muted-foreground">{info.name}</span>
            </p>
            <p className="text-muted-foreground">
              <span className="tabular-nums">{info.size}</span>, {info.use}
            </p>
          </div>
        </div>
      </Row>

      <Row disabled={disabled} term="Footage" onEdit={() => onEdit("footage")} editLabel="Edit footage">
        <p className="font-medium tabular-nums">
          {picked.length} {picked.length === 1 ? "clip" : "clips"}
          <span className="font-normal text-muted-foreground"> · {formatDuration(totalDurationMs(assets, assetIds))} total</span>
        </p>
        <ol className="mt-3 flex flex-wrap gap-2">
          {picked.map((asset, index) => (
            <li key={asset.id} className="relative w-16 overflow-hidden rounded-lg ring-1 ring-border">
              {asset.posterUrl ? (
                <img src={asset.posterUrl} alt={asset.title} title={asset.title} onError={onMediaError} className="aspect-4/5 w-full object-cover" />
              ) : (
                <div role="img" aria-label={asset.title} title={asset.title} className="aspect-4/5 w-full bg-wash" />
              )}
              <span className="absolute bottom-0.5 left-0.5 rounded bg-black/65 px-1 font-mono text-[10px] leading-4 text-white tabular-nums">
                {index + 1}
              </span>
            </li>
          ))}
        </ol>
      </Row>

      <Row disabled={disabled} term="Instructions" onEdit={() => onEdit("brief")} editLabel="Edit instructions">
        {vibe ? (
          <p className="mb-1">
            <span className="text-muted-foreground">Vibe: </span>
            <span className="font-medium">{vibe}</span>
          </p>
        ) : null}
        {title.trim() === "" ? null : <p className="mb-1 font-medium">{title.trim()}</p>}
        {text === "" ? (
          <p className="text-muted-foreground">None. I'll decide from your clips.</p>
        ) : (
          <p className="max-w-prose whitespace-pre-line">{text}</p>
        )}
      </Row>
      {costNote === undefined ? null : (
        <div className="grid gap-x-6 gap-y-2 p-4 sm:grid-cols-[8rem_1fr_auto]">
          <dt className="text-muted-foreground">Uses</dt>
          <dd className="min-w-0 text-muted-foreground">{costNote}</dd>
        </div>
      )}
    </dl>
  );
}

function Row({
  disabled,
  term,
  editLabel,
  onEdit,
  children,
}: {
  disabled: boolean;
  term: string;
  editLabel: string;
  onEdit: () => void;
  children: React.ReactNode;
}) {
  return (
    <div className="grid gap-x-6 gap-y-2 p-4 sm:grid-cols-[8rem_1fr_auto]">
      <dt className="text-muted-foreground">{term}</dt>
      <dd className="min-w-0">{children}</dd>
      <dd>
        <Button variant="ghost" size="sm" onClick={onEdit} aria-label={editLabel} disabled={disabled}>
          Edit
        </Button>
      </dd>
    </div>
  );
}
