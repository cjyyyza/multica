"use client";

import { useEffect, useState } from "react";
import { Loader2, Pencil } from "lucide-react";
import { isImeComposing } from "@multica/core/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { useT } from "../../i18n";

// Click-to-edit squad-wide concurrency cap. An empty input means unlimited
// (API null); 1-50 sets a hard cap shared by every agent in the squad.
export function MaxConcurrentTasksEditor({
  value,
  onSave,
}: {
  value: number | null;
  onSave: (next: number | null) => Promise<void>;
}) {
  const { t } = useT("squads");
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(value == null ? "" : String(value));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setDraft(value == null ? "" : String(value));
      setError(null);
    }
  }, [open, value]);

  const commit = async () => {
    const trimmed = draft.trim();
    let next: number | null = null;
    if (trimmed !== "") {
      const parsed = Number(trimmed);
      if (!Number.isInteger(parsed) || parsed < 1 || parsed > 50) {
        setError(t(($) => $.max_concurrent_tasks_dialog.invalid));
        return;
      }
      next = parsed;
    }
    if (next === value) {
      setOpen(false);
      return;
    }
    setSaving(true);
    try {
      await onSave(next);
      setOpen(false);
    } catch (err) {
      setError(err instanceof Error && err.message ? err.message : t(($) => $.name_editor.save_failed));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <button
            type="button"
            className="group -mx-1 inline-flex items-center gap-1.5 rounded-xs px-1 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
          >
            <span className="tabular-nums">
              {value ?? t(($) => $.max_concurrent_tasks_dialog.unlimited)}
            </span>
            <Pencil className="h-3 w-3 shrink-0 text-transparent transition-colors group-hover:text-muted-foreground" />
          </button>
        }
      />
      <PopoverContent align="start" className="w-64 p-3">
        <div className="space-y-2">
          <p className="text-caption font-medium">
            {t(($) => $.max_concurrent_tasks_dialog.title)}
          </p>
          <Input
            autoFocus
            type="number"
            min={1}
            max={50}
            inputMode="numeric"
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value);
              if (error) setError(null);
            }}
            placeholder={t(($) => $.max_concurrent_tasks_dialog.placeholder)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setOpen(false);
                return;
              }
              if (isImeComposing(e)) return;
              if (e.key === "Enter") {
                e.preventDefault();
                void commit();
              }
            }}
            className="h-8"
          />
          <p className="text-caption text-muted-foreground">
            {t(($) => $.max_concurrent_tasks_dialog.hint)}
          </p>
          {error && <p role="alert" className="text-caption text-destructive">{error}</p>}
          <div className="flex items-center justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={() => setOpen(false)} disabled={saving}>
              {t(($) => $.name_editor.cancel)}
            </Button>
            <Button size="sm" onClick={() => void commit()} disabled={saving}>
              {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : t(($) => $.name_editor.save)}
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}

