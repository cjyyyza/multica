"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useFeatureEnabled } from "@multica/core/config";
import { popoKeys } from "@multica/core/popo";
import type { PopoInstallation } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

export function RequirementIntakeSettings({ wsId, installation }: {
  wsId: string;
  installation: PopoInstallation;
}) {
  const enabled = useFeatureEnabled("requirement_flow");
  return enabled || installation.requirement_template_url
    ? <RequirementIntakeForm key={`${installation.id}:${installation.requirement_template_url ?? ""}`} wsId={wsId} installation={installation} />
    : null;
}

function RequirementIntakeForm({ wsId, installation }: {
  wsId: string;
  installation: PopoInstallation;
}) {
  const { t } = useT("requirements");
  const queryClient = useQueryClient();
  const [template, setTemplate] = useState(installation.requirement_template_url ?? "");
  const mutation = useMutation({
    mutationFn: () => api.setPopoRequirementIntake(wsId, installation.id, template.trim()),
    onSuccess: async (saved) => {
      if (saved.id !== installation.id || saved.status !== "active") {
        throw new Error(t(($) => $.intake_settings.save_failed));
      }
      await queryClient.invalidateQueries({ queryKey: popoKeys.all(wsId) });
    },
  });
  const inputId = `requirement-template-${installation.id}`;
  const hintId = `${inputId}-hint`;
  return (
    <form className="mt-3 space-y-2" onSubmit={(event) => { event.preventDefault(); mutation.mutate(); }}>
      <Label htmlFor={inputId}>{t(($) => $.intake_settings.template)}</Label>
      <div className="flex flex-wrap items-start gap-2">
        <Input id={inputId} type="url" value={template} aria-describedby={hintId}
          placeholder="https://dj01.pm.netease.com/issues/108402"
          className="min-w-0 flex-1" disabled={mutation.isPending}
          onChange={(event) => { setTemplate(event.target.value); mutation.reset(); }} />
        <Button type="submit" size="sm" disabled={mutation.isPending || template.trim() === (installation.requirement_template_url ?? "")}
          aria-busy={mutation.isPending}>
          {mutation.isPending ? t(($) => $.intake_settings.saving) : t(($) => $.intake_settings.save)}
        </Button>
      </div>
      <p id={hintId} className="text-caption text-muted-foreground">{t(($) => $.intake_settings.consequence)}</p>
      {mutation.error ? <p role="alert" className="text-caption text-destructive">{mutation.error.message}</p> : null}
      {mutation.isSuccess ? <p role="status" className="text-caption text-muted-foreground">{t(($) => $.intake_settings.saved)}</p> : null}
    </form>
  );
}
