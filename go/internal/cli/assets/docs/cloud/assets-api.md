# Assets API

The `AssetService` gRPC service manages the inventory of assets (devices, peripherals, etc.) registered in a Wendy Cloud organization.

## Proto package

`wendycloud.v1` — defined in `Proto/cloud/assets.proto`.

## Methods

### `CreateAsset`

```
CreateAsset(CreateAssetRequest) → Asset
```

Creates a new asset in the organization.

---

### `GetAsset`

```
GetAsset(GetAssetRequest) → Asset
```

Returns a single asset by ID.

---

### `UpdateAsset`

```
UpdateAsset(UpdateAssetRequest) → Asset
```

Updates mutable fields of an existing asset.

---

### `DeleteAsset`

```
DeleteAsset(DeleteAssetRequest) → DeleteAssetResponse
```

Deletes an asset by ID.

---

### `ListAssets` *(server-streaming)*

```
ListAssets(ListAssetsRequest) → stream ListAssetsResponse
```

Returns a stream of assets matching the request filters. Each streamed message contains one asset. The stream closes when all matching assets have been sent.

#### `ListAssetsRequest`

| Field | Type | Description |
|-------|------|-------------|
| `organization_id` | `int32` | *(required)* Organization to query. |
| `is_compute_device` | `optional bool` | When `true`, restricts results to compute devices. |
| `offset` | `optional int32` | Number of assets to skip (for manual pagination). |
| `limit` | `optional int32` | Maximum number of assets to return. |
| `filter` | `optional string` | Filter expression matching on name, details, asset_type, device_type, or tags. |
| `online_only` | `optional bool` | When `true`, only assets with an active broker presence are returned. |

#### `ListAssetsResponse` *(one message per streamed asset)*

| Field | Type | Description |
|-------|------|-------------|
| `asset` | `Asset` | The asset for this stream message. |
| `total` | `int32` | Total number of matching assets (may be sent on the first or last message). |

---

### `ListAssetChildren` *(server-streaming)*

```
ListAssetChildren(ListAssetChildrenRequest) → stream ListAssetChildrenResponse
```

Returns a stream of direct children of a given parent asset.

#### `ListAssetChildrenRequest`

| Field | Type | Description |
|-------|------|-------------|
| `parent_asset_id` | `int32` | *(required)* Parent asset ID. |
| `offset` | `optional int32` | Number of assets to skip. |
| `limit` | `optional int32` | Maximum number of assets to return. |

#### `ListAssetChildrenResponse` *(one message per streamed asset)*

| Field | Type | Description |
|-------|------|-------------|
| `asset` | `Asset` | The child asset for this stream message. |
| `total` | `int32` | Total number of matching children. |

---

### `GetAssetLineage`

```
GetAssetLineage(GetAssetLineageRequest) → GetAssetLineageResponse
```

Returns the full asset lineage (root asset, all intermediate assets) for a given asset ID.

## Streaming behaviour

`ListAssets` and `ListAssetChildren` are **server-streaming** RPCs. Clients must open a stream and call `Recv()` in a loop until `io.EOF` is returned:

```go
stream, err := client.ListAssets(ctx, req)
if err != nil {
    return err
}
var assets []*cloudpb.Asset
for {
    resp, err := stream.Recv()
    if err == io.EOF {
        break
    }
    if err != nil {
        return err
    }
    assets = append(assets, resp.GetAsset())
}
```

> **Migration note:** Prior to PR #601 `ListAssets` and `ListAssetChildren` were unary RPCs that returned a paginated response (`Assets []Asset`, `NextPageToken string`). They are now server-streaming. The old `page_size` / `page_token` fields have been replaced by `offset` / `limit`. Update any existing client code accordingly.

## Online-only filtering

Pass `online_only = true` to receive only devices with an active broker presence. The `wendy cloud discover` and `wendy cloud tunnel` commands both use this filter by default; `wendy cloud discover --all` omits it to include offline devices.
