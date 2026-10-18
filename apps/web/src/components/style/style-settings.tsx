import { Check } from "lucide-react";
import * as React from "react";
import type { CreatorStyle, StyleSettings } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { type ChoiceWords, colourName, isHexColour, SETTINGS, sourceNote, TITLE_COLOURS, templateFromWords, templateWords, validTemplate, withSetting } from "@/lib/style-words";
import { cn } from "@/lib/utils";

/**
 * The creator's settings as simple controls: one row per setting with what it does in a line, the choices as a segmented control (native
 * radios), and where the current value came from when it is not the creator's own ("Suggested from your videos"). Picking a value makes
 * it the creator's; "Use suggestion" gives it back to the suggestion.
 */
export function StyleSettingsForm({
  style,
  onChange,
  disabled = false,
}: {
  style: CreatorStyle;
  /** The whole new set of the creator's settings. */
  onChange: (settings: StyleSettings) => void;
  disabled?: boolean;
}) {
  const mine = style.settings;
  return (
    <div className="divide-y divide-hairline">
      {SETTINGS.map((setting) => (
        <ChoiceRow
          key={setting.key}
          setting={setting}
          value={String(style.effective[setting.key])}
          note={sourceNote(style.effective.sources[setting.key])}
          chosen={mine[setting.key] !== undefined}
          disabled={disabled}
          onPick={(value) => onChange(withSetting(mine, setting.key, value))}
          onReset={() => onChange(withSetting(mine, setting.key, null))}
        />
      ))}
      <TemplateRow
        value={style.effective.title_template}
        note={style.effective.title_template ? sourceNote(style.effective.sources.title_template) : "Default"}
        chosen={mine.title_template !== undefined}
        disabled={disabled}
        onPick={(value) => onChange(withSetting(mine, "title_template", value))}
        onReset={() => onChange(withSetting(mine, "title_template", null))}
      />
      <ColourRow
        value={style.effective.title_colour}
        note={style.effective.title_colour ? sourceNote(style.effective.sources.title_colour) : "Default"}
        chosen={mine.title_colour !== undefined}
        disabled={disabled}
        onPick={(value) => onChange(withSetting(mine, "title_colour", value))}
        onReset={() => onChange(withSetting(mine, "title_colour", null))}
      />
    </div>
  );
}

function RowHead({ label, explain, note, chosen, onReset, disabled, id }: { label: string; explain: string; note: string | null; chosen: boolean; onReset: () => void; disabled: boolean; id: string }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <div className="min-w-0">
        <p id={id} className="font-medium">
          {label}
        </p>
        <p className="text-[13px] text-muted-foreground text-pretty">{explain}</p>
      </div>
      {chosen ? (
        <button
          type="button"
          disabled={disabled}
          onClick={onReset}
          className="shrink-0 cursor-pointer rounded-md px-1.5 py-0.5 text-[12.5px] text-muted-foreground underline-offset-4 hover:text-foreground hover:underline disabled:cursor-default disabled:opacity-60"
        >
          Use suggestion
        </button>
      ) : note ? (
        <span className="shrink-0 rounded-full bg-wash px-2 py-0.5 text-[11.5px] font-medium text-subtle-foreground">{note}</span>
      ) : null}
    </div>
  );
}

function ChoiceRow({
  setting,
  value,
  note,
  chosen,
  disabled,
  onPick,
  onReset,
}: {
  setting: ChoiceWords;
  value: string;
  note: string | null;
  chosen: boolean;
  disabled: boolean;
  onPick: (value: string) => void;
  onReset: () => void;
}) {
  const name = React.useId();
  return (
    <div className="space-y-2.5 py-3.5 first:pt-0 last:pb-0">
      <RowHead id={`${name}-label`} label={setting.label} explain={setting.explain} note={note} chosen={chosen} onReset={onReset} disabled={disabled} />
      <div role="radiogroup" aria-labelledby={`${name}-label`} className="flex flex-wrap gap-1 rounded-xl bg-well p-1 shadow-well">
        {setting.options.map((option) => (
          <label
            key={option.value}
            className={cn(
              "relative flex min-h-9 flex-1 cursor-pointer items-center justify-center rounded-lg px-2.5 text-center text-[13.5px] font-medium whitespace-nowrap text-muted-foreground select-none",
              "transition-[color,background-color,box-shadow] duration-(--dur-fast) hover:text-foreground",
              "has-checked:bg-surface-raised has-checked:text-foreground has-checked:shadow-raised",
              "has-focus-visible:ring-2 has-focus-visible:ring-brand has-disabled:cursor-not-allowed has-disabled:opacity-60",
            )}
          >
            <input type="radio" name={name} value={option.value} checked={option.value === value} disabled={disabled} onChange={() => onPick(option.value)} className="sr-only" />
            {option.label}
          </label>
        ))}
      </div>
    </div>
  );
}

function ColourRow({
  value,
  note,
  chosen,
  disabled,
  onPick,
  onReset,
}: {
  value: string | null;
  note: string | null;
  chosen: boolean;
  disabled: boolean;
  onPick: (value: string) => void;
  onReset: () => void;
}) {
  const name = React.useId();
  const custom = value && !TITLE_COLOURS.some((c) => c.value.toLowerCase() === value.toLowerCase()) ? value.toUpperCase() : null;
  const swatches = custom ? [...TITLE_COLOURS, { value: custom, label: "Your colour" }] : TITLE_COLOURS;
  return (
    <div className="space-y-2.5 py-3.5 last:pb-0">
      <RowHead
        id={`${name}-label`}
        label="Title colour"
        explain={value ? `The letters of your titles: ${colourName(value)}.` : "The letters of your titles. The editor picks one when you don't."}
        note={note}
        chosen={chosen}
        onReset={onReset}
        disabled={disabled}
      />
      <div role="radiogroup" aria-labelledby={`${name}-label`} className="flex flex-wrap items-center gap-2">
        {swatches.map((colour) => {
          const checked = value?.toLowerCase() === colour.value.toLowerCase();
          return (
            <label
              key={colour.value}
              title={colour.label}
              className="group/swatch relative flex size-9 cursor-pointer items-center justify-center rounded-full ring-1 ring-border has-checked:ring-2 has-checked:ring-brand has-checked:ring-offset-2 has-checked:ring-offset-background has-focus-visible:ring-2 has-focus-visible:ring-brand has-disabled:cursor-not-allowed has-disabled:opacity-60"
              style={{ backgroundColor: colour.value }}
            >
              <input type="radio" name={name} value={colour.value} checked={checked} disabled={disabled} onChange={() => onPick(colour.value)} className="sr-only" />
              <span className="sr-only">{colour.label}</span>
              {checked ? <Check aria-hidden className="size-4 mix-blend-difference text-white" strokeWidth={3} /> : null}
            </label>
          );
        })}
        <label className="flex h-9 cursor-pointer items-center gap-2 rounded-full bg-wash px-3 text-[13px] text-muted-foreground ring-1 ring-border hover:text-foreground has-focus-visible:ring-2 has-focus-visible:ring-brand">
          <span>Other…</span>
          <input
            type="color"
            disabled={disabled}
            value={value && isHexColour(value) ? value.toLowerCase() : "#ffffff"}
            onChange={(event) => isHexColour(event.target.value) && onPick(event.target.value.toUpperCase())}
            className="size-5 cursor-pointer rounded border-0 bg-transparent p-0"
            aria-label="Pick another colour"
          />
        </label>
      </div>
    </div>
  );
}

function TemplateRow({
  value,
  note,
  chosen,
  disabled,
  onPick,
  onReset,
}: {
  value: string | null;
  note: string | null;
  chosen: boolean;
  disabled: boolean;
  onPick: (value: string) => void;
  onReset: () => void;
}) {
  const id = React.useId();
  const [text, setText] = React.useState(value ? templateWords(value) : "");
  React.useEffect(() => setText(value ? templateWords(value) : ""), [value]);
  const changed = text.trim() !== (value ? templateWords(value) : "");
  const ok = validTemplate(text);
  return (
    <div className="space-y-2.5 py-3.5">
      <RowHead
        id={`${id}-label`}
        label="Title form"
        explain="How your titles are built. The words in <brackets> change with each video."
        note={note}
        chosen={chosen}
        onReset={onReset}
        disabled={disabled}
      />
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          if (ok && changed) onPick(templateFromWords(text));
        }}
      >
        <Input
          aria-labelledby={`${id}-label`}
          value={text}
          disabled={disabled}
          maxLength={80}
          placeholder="<topic> <number>"
          onChange={(event) => setText(event.target.value)}
          className="min-w-0 flex-1 font-medium"
        />
        <Button type="submit" variant="secondary" size="sm" disabled={disabled || !changed || !ok}>
          Save
        </Button>
      </form>
      {text.trim() !== "" && !ok ? <p className="text-[12.5px] text-destructive">Use letters, numbers and words in &lt;brackets&gt;, up to 80 characters.</p> : null}
    </div>
  );
}
