import type { P4Changelist, P4ChangelistStatus } from "../types";

/** One changelist as a published result line needs it, locale copy aside. */
export interface P4ChangelistSummaryEntry {
  number: number;
  status: P4ChangelistStatus;
  /** The pending number a submit replaced; null when the number never moved. */
  originalNumber: number | null;
  swarmReviewNumber: number | null;
}

/**
 * The changelists worth naming when publishing a result back to 易协作, newest
 * first. Deleted changelists and rows without a usable number are left out:
 * both would name work the reader cannot look up.
 */
export function p4ChangelistSummaryEntries(changelists: P4Changelist[]): P4ChangelistSummaryEntry[] {
  return changelists
    .filter((cl) => cl.status !== "deleted" && cl.number > 0)
    .map((cl) => ({
      number: cl.number,
      status: cl.status,
      originalNumber:
        cl.original_number > 0 && cl.original_number !== cl.number ? cl.original_number : null,
      swarmReviewNumber:
        cl.swarm_review_number !== null && cl.swarm_review_number > 0
          ? cl.swarm_review_number
          : null,
    }));
}
