import { expect, test, vi } from "vitest";
import { ApiError } from "@/lib/api";
import { ME_QUERY_KEY, PLAN_QUERY_KEY, createQueryClient, isPlanRequired } from "@/lib/query-client";

test("only the API's 402 plan_required counts", () => {
  expect(isPlanRequired(new ApiError(402, "plan_required", "no plan"))).toBe(true);
  expect(isPlanRequired(new ApiError(402, "something_else", "x"))).toBe(false);
  expect(isPlanRequired(new ApiError(403, "plan_required", "x"))).toBe(false);
  expect(isPlanRequired(new Error("plan_required"))).toBe(false);
  expect(isPlanRequired(null)).toBe(false);
});

test("a 402 plan_required from any query or mutation fetches `me` again, so the paywall appears without a reload", async () => {
  const client = createQueryClient();
  const meFetches = vi.fn(async () => ({ n: 1 }));
  await client.fetchQuery({ queryKey: [ME_QUERY_KEY, "user_1", "org_1"], queryFn: meFetches });
  expect(client.getQueryState([ME_QUERY_KEY, "user_1", "org_1"])?.isInvalidated).toBe(false);

  // Another call is refused with 402: `me` is marked stale.
  await expect(
    client.fetchQuery({
      queryKey: ["library"],
      queryFn: async () => {
        throw new ApiError(402, "plan_required", "this workspace has no active plan");
      },
      retry: false,
    }),
  ).rejects.toBeInstanceOf(ApiError);
  expect(client.getQueryState([ME_QUERY_KEY, "user_1", "org_1"])?.isInvalidated).toBe(true);

  // The same for a mutation.
  const other = createQueryClient();
  await other.fetchQuery({ queryKey: [ME_QUERY_KEY, "u", "o"], queryFn: meFetches });
  await expect(
    other
      .getMutationCache()
      .build(other, {
        mutationFn: async () => {
          throw new ApiError(402, "plan_required", "no plan");
        },
      })
      .execute(undefined),
  ).rejects.toBeInstanceOf(ApiError);
  expect(other.getQueryState([ME_QUERY_KEY, "u", "o"])?.isInvalidated).toBe(true);
});

test("other errors leave `me` alone", async () => {
  const client = createQueryClient();
  await client.fetchQuery({ queryKey: [ME_QUERY_KEY, "u", "o"], queryFn: async () => 1 });
  await expect(
    client.fetchQuery({
      queryKey: ["library"],
      queryFn: async () => {
        throw new ApiError(500, "internal_error", "x");
      },
      retry: false,
    }),
  ).rejects.toBeInstanceOf(ApiError);
  expect(client.getQueryState([ME_QUERY_KEY, "u", "o"])?.isInvalidated).toBe(false);
});

test("the plan query is fetched again with `me`, and a query with another key is left alone", async () => {
  const client = createQueryClient();
  await client.fetchQuery({ queryKey: [ME_QUERY_KEY, "u", "o"], queryFn: async () => 1 });
  await client.fetchQuery({ queryKey: [PLAN_QUERY_KEY, "u", "o"], queryFn: async () => 1 });
  await client.fetchQuery({ queryKey: ["other"], queryFn: async () => 1 });
  await expect(
    client.fetchQuery({
      queryKey: ["library-summary", "u", "o"],
      queryFn: async () => {
        throw new ApiError(402, "plan_required", "no plan");
      },
      retry: false,
    }),
  ).rejects.toBeInstanceOf(ApiError);
  expect(client.getQueryState([PLAN_QUERY_KEY, "u", "o"])?.isInvalidated).toBe(true);
  expect(client.getQueryState([ME_QUERY_KEY, "u", "o"])?.isInvalidated).toBe(true);
  expect(client.getQueryState(["other"])?.isInvalidated).toBe(false);
});
