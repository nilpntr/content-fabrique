/** Stands in for `src/lib/session.ts`: the fixtures viewer is always signed in. */
export const fetchSession = async () => ({ isAuthenticated: true, userId: "user_test", orgId: "org_test" });
