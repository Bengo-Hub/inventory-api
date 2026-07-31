# Inventory API - Barcode & Label Printing

**Last updated**: 2026-07-31
**Module**: `internal/modules/barcode/` (`encode.go`, `gs1.go`, `label.go`, `avery_pdf.go`, `thermal.go`)
**Handler**: `internal/http/handlers/barcode.go`
**Endpoints**: `GET /inventory/items/{itemID}/barcode.png`, `POST /inventory/labels/print`

This is a reference doc for whoever next extends label printing: what real-world printer/label
conventions this module is built against, what it actually supports today, and why the label
layout looks the way it does. It is not a tutorial on the HTTP API — see the handler's request
struct (`PrintLabelsRequest`) for wire-format details.

---

## Table of Contents

1. [Printer types](#printer-types)
2. [Label sizes and DPI](#label-sizes-and-dpi)
3. [Symbologies supported](#symbologies-supported)
4. [GS1-128 sizing rules](#gs1-128-sizing-rules)
5. [What this codebase supports today](#what-this-codebase-supports-today)
6. [Design decision: one card, not sections](#design-decision-one-card-not-sections)
7. [Design decision: dashed cut guide](#design-decision-dashed-cut-guide)

---

## Printer types

Two fundamentally different physical setups produce "a label," and the `format` field in
`POST /inventory/labels/print` picks between them:

- **Thermal desktop label printers** (Zebra, TSC, Brother QL, DYMO LabelWriter, Xprinter, …).
  These print one label at a time from a roll (or fanfold stack), with no ink or toner —
  either **direct-thermal** (heat-sensitive paper; the print fades over time/heat/UV exposure,
  which is fine for short-lived shelf labels but a bad choice for long-lived asset tags) or
  **thermal-transfer** (a wax/resin ribbon melted onto plain label stock; more durable, needed
  for asset/serial labels that must survive months in a warehouse). The roll auto-cuts or
  tears off along a perforation after each label — there's no "sheet," so no cut guide is
  needed. `format: "thermal_zpl"` targets this category, emitting Zebra ZPL II
  (`^XA...^XZ` per label). `format: "dymo"` targets DYMO's own family, which is usually driven
  through a host-side SDK/bridge rather than raw ZPL, so the module emits a simple deterministic
  text stream instead (see `renderDymo` in `thermal.go`) for a bridge process to translate.
- **Pre-cut adhesive label sheets on a regular inkjet/laser printer** (the Avery convention).
  A full A4/Letter page feeds through an ordinary office printer; the labels are arranged in a
  grid on the page. Genuine Avery-branded stock is die-cut so each label peels off cleanly, but
  in practice a lot of this is printed onto **plain, uncut paper** (cheaper, or because the
  exact branded SKU isn't on hand) and then has to be hand-cut — hence `avery_a4`'s dashed
  "cut here" guide (see [below](#design-decision-dashed-cut-guide)). `format: "avery_a4"`
  targets this category and produces a PDF sheet.

## Label sizes and DPI

**Thermal** (`thermal_size` field, resolved by `ThermalSpecByName` in `thermal.go`), all at
203dpi:

| Preset | Size | When to use |
|---|---|---|
| `2x1` | 2"×1" | Simple SKU/barcode label — the standard small thermal label size (matches common DYMO/Zebra small-label stock). No room for a dense GS1-128 payload. |
| `3x2` | 3"×2" | Recommended once `include_lot`/`include_serial` is set — GS1-128 payloads are longer and denser, and read more reliably with the extra width/height. |
| `4x2` (default) | 4"×2" | General-purpose item label; the pre-existing default kept so old callers with no opinion on size keep working. |
| `4x6` | 4"×6" | Shipping-label format — matches the de-facto standard size used by carrier label printers (FedEx/UPS/USPS thermal shipping labels are all 4"×6"). |

**DPI**: 203dpi is the common/standard resolution for desktop thermal printers and is sufficient
for standard 1D barcodes (EAN-13, Code128, GS1-128) and normal label text. 300dpi thermal
printers exist and are preferable when a label carries small text or very dense codes (e.g. a
2D code at high data density), but this module hard-codes 203dpi in every `ThermalSpec` — there
is no 300dpi preset today.

**Avery sheet** (`sheet` field, resolved by `AverySpecByName` in `label.go`):

| Preset | Paper | Grid | Label size | Region fit |
|---|---|---|---|---|
| `l7160` (default) | A4 | 3 cols × 7 rows = 21/sheet | 63.5×38.1mm | Most of the world outside North America (A4 is the standard office paper size there). |
| `5160` | US Letter | 3 cols × 10 rows = 30/sheet | 66.7×25.4mm (2-⅝"×1") | US/Canada (Letter is the standard office paper size there). Same physical grid shared by Avery 5160/8160/5260/8460 — those SKUs differ in adhesive/finish, not layout, which is why one `AverySpec` covers all four. |

## Symbologies supported

From `encode.go` and `gs1.go`:

- **EAN-13 / UPC-A** (`SymEAN13` / `SymUPCA`) — standard retail barcodes. `NormalizeEAN13`
  validates/completes a 12 or 13-digit code; `GenerateInternalEAN13` mints an internal code in
  the GS1 restricted-circulation prefix range (`20`–`29`) for in-house items with no
  manufacturer GTIN. These are valid for in-store scanning only, never published to the global
  GS1 registry.
- **Code 128** (`SymCode128`) — alphanumeric SKUs that don't fit EAN-13's numeric-only format.
- **GS1-128** (`SymGS1128`) — Code 128 carrying GS1 Application Identifiers (AIs), built via
  `GS1Builder` in `gs1.go`: `(01)` GTIN, `(10)` batch/lot, `(17)` expiry (`YYMMDD`), `(21)`
  serial, `(3103)` net weight in kg, `(392n)` price with `n` implied decimals. Selected
  automatically by the handler when the request sets `include_lot` or `include_serial`.

`ChooseSymbology` in `encode.go` auto-picks EAN-13 for 12/13-digit numeric codes and Code 128
otherwise, when a label isn't going through the GS1 path.

## GS1-128 sizing rules

The GS1 General Specifications define physical constraints for a GS1-128 symbol beyond just
the data content — commonly cited figures are:

- **Quiet zone**: minimum 10x on each side of the barcode, where x is the narrow-bar (module)
  width actually printed.
- **Max height**: 33.75mm.
- **Min X-dimension**: on the order of 0.25mm, below which many scanners can't reliably resolve
  bars.

**What `gs1.go` actually implements**: `GS1Builder` is entirely about the *data* layer — AI
concatenation, FNC1 placement (`fixedLenAIs` correctly distinguishes fixed-length AIs like `01`
GTIN and the `31nn`/`32nn` decimal-measure family, which don't need a trailing FNC1 separator,
from variable-length ones like `10` batch/`21` serial, which do). It does not compute or enforce
any of the physical rules above — there is no quiet-zone margin calculation, no height cap, and
no X-dimension check anywhere in the GS1 code path.

Looking at where the barcode is actually rendered:

- `RenderPNG` (`encode.go`) just scales the boombuler-encoded barcode to the caller-supplied
  pixel `width`/`height` — no explicit quiet-zone padding is added beyond whatever the
  boombuler library itself emits.
- `drawLabelCell` (`avery_pdf.go`) computes `barH` as "whatever vertical space is left in the
  cell after text rows," with a 6mm floor but **no ceiling** — so on a large label with little
  text, the barcode image could exceed the 33.75mm GS1 max height with nothing catching it.
- `renderZPL` (`thermal.go`) does the same thing in dots (`barH = maxInt((h-y)/2, 60)`), again
  uncapped.

**Flagging for future work** (docs-only pass, not fixed here): if this module starts printing
dense GS1-128 labels at scale, it's worth adding an explicit height cap and a quiet-zone margin
calculation tied to the actual module width used, and validating against a real barcode verifier
rather than relying on "there was enough blank space in the cell." Today, correctness of a
printed GS1-128 label's quiet zone/height depends entirely on the label preset chosen (bigger
presets have more incidental margin) rather than on anything computed from the GS1 spec.

## What this codebase supports today

`POST /inventory/labels/print` (`PrintLabelsRequest` in `barcode.go`):

| Field | Values | Effect |
|---|---|---|
| `format` | `avery_a4` \| `thermal_zpl` \| `dymo` | Output format (PDF vs. printer command text). |
| `sheet` | `l7160` (default) \| `5160` | Avery grid preset, only used when `format: "avery_a4"`. |
| `thermal_size` | `2x1` \| `3x2` \| `4x2` (default) \| `4x6` | Physical label size, only used when `format: "thermal_zpl"`. |
| `include_lot` | bool | One GS1-128 label per active lot of each selected item, embedding `(10)`/`(17)`. |
| `include_serial` | bool | One GS1-128 label per available serial, embedding `(21)`. |
| `include_price` | bool | Adds `(392n)` to the GS1 payload and a printed price line. |
| `price_decimals`, `currency` | | Formatting for the printed price line and the `(392n)` implied-decimals value. |
| `category_id` / `supplier_id` / `purchase_order_id` / `item_ids` | | Selection — exactly one drives which items get labelled. |
| `qty_per_item`, `quantities` | | How many copies of each item's label to emit. |

`GET /inventory/items/{itemID}/barcode.png` renders a single item's barcode as a PNG, lazily
generating and persisting an internal EAN-13 if the item has none.

## Design decision: one card, not sections

Each Avery-cell/thermal label renders as **one stacked column of rows** — tenant name, title,
SKU, an optional lot/serial+expiry detail line, the barcode image, the human-readable barcode
text, and an optional price — rather than a colored, multi-section banner design. Reasons
(see comments in `drawLabelCell`/`renderZPL`):

- **Space.** The smallest supported preset is a 2"×1" thermal label. A colored header band or
  boxed sections eat into the already-tight vertical budget that the barcode's own quiet zone
  and human-readable text need; on a label this small, decoration directly trades off against
  legibility.
- **Toner/ribbon cost at scale.** These labels print in the thousands (a full purchase order's
  worth of items, or every lot/serial of a batch). A solid colored banner burns toner/ribbon
  across every single print; a compact one-line tenant name in plain text does not.
- **Each field earns its own row.** SKU and the lot/serial+expiry detail used to be crammed onto
  one sub-title line; they're now separate rows (SKU left-aligned, the lot/detail line
  right-aligned) so a label with both a SKU and an expiry date reads as two distinct pieces of
  information instead of one truncated string.

## Design decision: dashed cut guide

`drawCutGuide` in `avery_pdf.go` draws a dashed (not solid) rectangle around every label cell
on the Avery sheet. Two reasons:

- **Why a guide at all**: genuine die-cut Avery stock doesn't need one (the label peels off
  along its own edge), but these sheets are routinely printed on plain, non-perforated paper —
  in which case there's no physical cue for where to cut, and a guide is the only way to get
  clean rows/columns by hand.
- **Why dashed, not solid**: a solid rectangle reads as part of the label's own design (a
  border), which is confusing on stock that *is* pre-cut (redundant line, wrong place if
  slightly misaligned) and looks unfinished if left on a label someone doesn't intend to trim.
  A dashed line follows the same visual convention as tear-lines/perforation-marks elsewhere
  (ticket stubs, coupons) — it reads unambiguously as "cut along here," not as label content.
