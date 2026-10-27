"use client";

import { useRef } from "react";
import { createSafeId } from "@multica/core/utils";
import { useReconcileRequirement, type RequirementDetail } from "@multica/core/requirements";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

const RECONCILE_STATES = new Set(["unknown", "needs_reconcile", "failed"]);
const READBACK_OPERATION_KINDS = new Set(["publish", "writeback"]);

type ReconcileTarget =
  | { key: string; operationId: string; deliveryId?: string }
  | { key: string; operationId?: undefined; deliveryId: string };

export function RequirementSyncStatus({ wsId, detail }: { wsId: string; detail: RequirementDetail }) {
  const { t } = useT("requirements");
  const reconcile = useReconcileRequirement(wsId, detail.id);
  const keys = useRef(new Map<string, string>());
  if (!detail.lastError && detail.operations.length === 0 && detail.deliveries.length === 0) return null;

  const start = ({ key: targetKey, ...request }: ReconcileTarget) => {
    const key = keys.current.get(targetKey) ?? createSafeId();
    keys.current.set(targetKey, key);
    void reconcile.mutateAsync({ ...request, idempotencyKey: key }).then(
      () => keys.current.delete(targetKey),
      () => undefined,
    );
  };

  return (
    <section aria-label={t(($) => $.sync.title)} className="space-y-3 rounded-lg border border-border p-3">
      <h3 className="text-body font-medium">{t(($) => $.sync.title)}</h3>
      {detail.lastError && <p role="alert" className="text-caption text-destructive">{detail.lastError}</p>}
      {detail.operations.length > 0 && <ul className="space-y-2" aria-label={t(($) => $.sync.operations)}>
        {detail.operations.map((operation) => {
          const canReadback = detail.permissions.canReconcile && READBACK_OPERATION_KINDS.has(operation.kind) && RECONCILE_STATES.has(operation.state);
          return (
            <li key={operation.id} className="space-y-1 text-caption">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span>{operation.kind} · {operation.state}</span>
                {canReadback && (
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={reconcile.isPending}
                    aria-busy={reconcile.isPending}
                    onClick={() => start({ key: `operation:${operation.id}`, operationId: operation.id })}
                  >
                    {t(($) => $.sync.reconcile_operation)}
                  </Button>
                )}
              </div>
              {operation.state === "unknown" && (
                <p className="text-muted-foreground">{t(($) => $.sync.no_auto_retry)}</p>
              )}
              {operation.requestHash && <p className="break-all font-mono">{t(($) => $.sync.request_hash)}: {operation.requestHash}</p>}
              {operation.error && <p role="alert" className="text-destructive">{operation.error}</p>}
            </li>
          );
        })}
      </ul>}
      {detail.deliveries.length > 0 && <ul className="space-y-2 text-caption" aria-label={t(($) => $.sync.deliveries)}>
        {detail.deliveries.map((delivery) => {
          const canReconcile = detail.permissions.canReconcile && RECONCILE_STATES.has(delivery.state);
          return (
            <li key={delivery.id} className="space-y-1">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span>{delivery.kind} · {delivery.state}</span>
                {canReconcile && (
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={reconcile.isPending}
                    aria-busy={reconcile.isPending}
                    onClick={() => start({ key: `delivery:${delivery.id}`, deliveryId: delivery.id })}
                  >
                    {t(($) => $.sync.reconcile_delivery)}
                  </Button>
                )}
              </div>
              {delivery.state === "unknown" && (
                <p className="text-muted-foreground">{t(($) => $.sync.no_auto_retry)}</p>
              )}
              {delivery.lastError && <p role="alert" className="text-destructive">{delivery.lastError}</p>}
            </li>
          );
        })}
      </ul>}
      {reconcile.error && <p role="alert" className="text-caption text-destructive">{reconcile.error.message}</p>}
    </section>
  );
}
