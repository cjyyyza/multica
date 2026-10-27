import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import {
  invalidateApprovedRequirementIssue,
  invalidateOpenedRequirementChat,
  invalidateRequirementDetail,
  writeAuthoritativeDetail,
} from "./cache";
import type {
  RequirementApproveResponse,
  RequirementDetail,
  RequirementOpenResponse,
} from "./schemas";
import type {
  AcceptRequirementRequest,
  ApproveRequirementRequest,
  CancelRequirementRequest,
  CreateRequirementRequest,
  OpenRequirementRequest,
  ReconcileRequirementRequest,
  RejectRequirementRequest,
  RequirementMutationInput,
  SubmitRequirementCandidateRequest,
  UpdateRequirementParticipantsRequest,
} from "./types";

function useRequirementMutation<Request, Result>(
  wsId: string,
  requirementId: string,
  request: (input: RequirementMutationInput<Request>) => Promise<Result>,
  detailFromResult: (result: Result) => RequirementDetail,
  afterSuccess?: (qc: QueryClient, result: Result) => void,
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: request,
    onSuccess: (result) => {
      writeAuthoritativeDetail(qc, wsId, detailFromResult(result));
      afterSuccess?.(qc, result);
    },
    onSettled: () => {
      invalidateRequirementDetail(qc, wsId, requirementId);
    },
  });
}

export function useCreateRequirement(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: RequirementMutationInput<CreateRequirementRequest>) =>
      api.createRequirement(wsId, input),
    onSuccess: (detail) => {
      writeAuthoritativeDetail(qc, wsId, detail);
    },
    onSettled: (detail) => {
      if (detail) invalidateRequirementDetail(qc, wsId, detail.id);
    },
  });
}

export function useUpdateRequirementParticipants(
  wsId: string,
  requirementId: string,
) {
  return useRequirementMutation(
    wsId,
    requirementId,
    (input: RequirementMutationInput<UpdateRequirementParticipantsRequest>) =>
      api.updateRequirementParticipants(wsId, requirementId, input),
    (detail: RequirementDetail) => detail,
  );
}

export function useOpenRequirement(wsId: string, requirementId: string) {
  return useRequirementMutation(
    wsId,
    requirementId,
    (input: RequirementMutationInput<OpenRequirementRequest>) =>
      api.openRequirement(wsId, requirementId, input),
    (result: RequirementOpenResponse) => result.requirement,
    (qc, result) => invalidateOpenedRequirementChat(qc, wsId, result),
  );
}

export function useSubmitRequirementCandidate(
  wsId: string,
  requirementId: string,
) {
  return useRequirementMutation(
    wsId,
    requirementId,
    (input: RequirementMutationInput<SubmitRequirementCandidateRequest>) =>
      api.submitRequirementCandidate(wsId, requirementId, input),
    (detail: RequirementDetail) => detail,
  );
}

export function useApproveRequirement(wsId: string, requirementId: string) {
  return useRequirementMutation(
    wsId,
    requirementId,
    (input: RequirementMutationInput<ApproveRequirementRequest>) =>
      api.approveRequirement(wsId, requirementId, input),
    (result: RequirementApproveResponse) => result.requirement,
    (qc, result) => invalidateApprovedRequirementIssue(qc, wsId, result),
  );
}

export function useAcceptRequirement(wsId: string, requirementId: string) {
  return useRequirementMutation(
    wsId,
    requirementId,
    (input: RequirementMutationInput<AcceptRequirementRequest>) =>
      api.acceptRequirement(wsId, requirementId, input),
    (detail: RequirementDetail) => detail,
  );
}

export function useRejectRequirement(wsId: string, requirementId: string) {
  return useRequirementMutation(
    wsId,
    requirementId,
    (input: RequirementMutationInput<RejectRequirementRequest>) =>
      api.rejectRequirement(wsId, requirementId, input),
    (detail: RequirementDetail) => detail,
  );
}

export function useCancelRequirement(wsId: string, requirementId: string) {
  return useRequirementMutation(
    wsId,
    requirementId,
    (input: RequirementMutationInput<CancelRequirementRequest>) =>
      api.cancelRequirement(wsId, requirementId, input),
    (detail: RequirementDetail) => detail,
  );
}

export function useReconcileRequirement(wsId: string, requirementId: string) {
  return useRequirementMutation(
    wsId,
    requirementId,
    (input: RequirementMutationInput<ReconcileRequirementRequest>) =>
      api.reconcileRequirement(wsId, requirementId, input),
    (detail: RequirementDetail) => detail,
  );
}
