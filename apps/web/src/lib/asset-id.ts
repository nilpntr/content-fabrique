/** An asset's public id: the `ast_` prefix and 26 characters of base32 (D-24). A value that is not one is not an asset to open. */
export const isAssetId = (value: unknown): value is string => typeof value === "string" && /^ast_[0-9a-z]{26}$/.test(value);
