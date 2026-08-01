# Inventory API - Barcode & Label Printing

**Last updated**: 2026-08-01
**Module**: `internal/modules/barcode/` (`encode.go`, `gs1.go`, `label.go`, `avery_pdf.go`,
`thermal.go`, `tspl.go`, `template.go`)
**Handler**: `internal/http/handlers/barcode.go`
**Endpoints**: `GET /inventory/items/{itemID}/barcode.png`, `GET /inventory/items/{itemID}/label.pdf`,
`POST /inventory/labels/print`

This is a reference doc for whoever next extends label printing: what real-world printer/label
conventions this module is built against, what it actually supports today, and why the label
layout looks the way it does. It is not a tutorial on the HTTP API — see the handler's request
struct (`PrintLabelsRequest`) for wire-format details.

---

## Table of Contents

1. [Printer types](#printer-types)
2. [Label templates: physical size, rows/lanes, DPI](#label-templates-physical-size-rowslanes-dpi)
3. [The rotation bug and how Rotate fixes it](#the-rotation-bug-and-how-rotate-fixes-it)
4. [TSPL support (Xprinter/TSC-compatible printers)](#tspl-support-xprintertsc-compatible-printers)
5. [Direct USB printing via the local print-agent](#direct-usb-printing-via-the-local-print-agent)
6. [Symbologies supported](#symbologies-supported)
7. [GS1-128 sizing rules](#gs1-128-sizing-rules)
8. [What this codebase supports today](#what-this-codebase-supports-today)
9. [Design decision: one card, not sections](#design-decision-one-card-not-sections)
10. [Design decision: dashed cut guide](#design-decision-dashed-cut-guide)
11. [Scope decision: no shared Go module (yet)](#scope-decision-no-shared-go-module-yet)

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
  needed. Two command languages are supported, because they are NOT interchangeable — sending
  the wrong one to a printer either does nothing or prints garbage:
  - `format: "thermal_zpl"` — Zebra ZPL II (`^XA...^XZ` per feed-row). Targets genuine Zebra
    printers (and any printer explicitly running in a ZPL-emulation mode).
  - `format: "thermal_tspl"` — TSC/TSPL2 (`SIZE`/`GAP`/`CLS`/…/`PRINT` per feed-row). Targets
    TSC-compatible desktop printers — **including the Xprinter XP-330B**, confirmed via
    Xprinter's own TSPL-emulation spec. Xprinter printers do **not** speak ZPL natively; if
    your printer is an Xprinter (or another TSC-clone) model, use `thermal_tspl`, not
    `thermal_zpl`. See [§4](#tspl-support-xprintertsc-compatible-printers).
  - `format: "dymo"` targets DYMO's own family, which is usually driven through a host-side
    SDK/bridge rather than raw ZPL/TSPL, so the module emits a simple deterministic text stream
    instead (see `renderDymo` in `thermal.go`) for a bridge process to translate.
- **Pre-cut adhesive label sheets on a regular inkjet/laser printer** (the Avery convention).
  A full A4/Letter page feeds through an ordinary office printer; the labels are arranged in a
  grid on the page. Genuine Avery-branded stock is die-cut so each label peels off cleanly, but
  in practice a lot of this is printed onto **plain, uncut paper** (cheaper, or because the
  exact branded SKU isn't on hand) and then has to be hand-cut — hence `avery_a4`'s dashed
  "cut here" guide (see [below](#design-decision-dashed-cut-guide)). `format: "avery_a4"`
  targets this category and produces a PDF sheet.

## Label templates: physical size, rows/lanes, DPI

**Thermal** (`template` field, resolved by `LabelTemplateByName` in `template.go` — replaces the
old `ThermalSpec`/`thermal_size`, still accepted for back-compat), all at 203dpi:

| Preset | Rows (lanes) | One label's size | When to use |
|---|---|---|---|
| `2x1` | 1 | 2"×1" | Simple SKU/barcode label — the standard small thermal label size. No room for a dense GS1-128 payload. |
| `3x2` | 1 | 3"×2" | Recommended once `include_lot`/`include_serial` is set — GS1-128 payloads are longer and denser, and read more reliably with the extra width/height. |
| `4x2` (default) | 1 | 4"×2" | General-purpose item label; the pre-existing default kept so old callers with no opinion on size keep working. |
| `4x6` | 1 | 4"×6" | Shipping-label format — matches the de-facto standard size used by carrier label printers (FedEx/UPS/USPS thermal shipping labels are all 4"×6"). |
| `1row_40x30` | 1 | 40×30mm | A single narrow lane feeding one label at a time down its length — engineering estimate, **not** bench-confirmed (see `1row_29x62` below for the one preset that actually is). |
| `2row_38x30` | 2 | 38×30mm each | A wider roll die-cut into 2 parallel lanes side-by-side. |
| `3row_25x40` | 3 | 25×40mm each | 3 parallel lanes. |
| `4row_18x30` | 4 | 18×30mm each | 4 parallel lanes. |
| `1row_29x62` | 1 | 29×62mm | **Bench-verified** 2026-08-02 by printing directly to an Xprinter XP-330B via the local print-agent — the actual physical roll size on the desk this was tested against (a single narrow ~29mm lane, ~62mm per label). Not made the default (inventory tenants' real stock varies far more than this one bench setup) — pick it explicitly, or set it as your tenant's saved default in Settings, if your roll matches. |
| `custom` | 1-4 (`custom_lanes`) | `custom_label_w_in`/`custom_label_h_in` | Explicit W/H/lanes/gaps/rotate for a real roll that doesn't match any preset above. |

**"Rows" = lanes across the roll's width, not the Avery sheet's grid rows.** The multi-row
presets above are **engineering estimates** sized to fit the Xprinter XP-330B's confirmed
≤80mm media width — they are **not vendor-confirmed exact Xprinter SKUs**. If your real label
stock doesn't match one of these exactly, measure it and use `template: "custom"` with
`custom_label_w_in`/`custom_label_h_in`/`custom_lanes`/`custom_gap_x_in`/`custom_gap_y_in`
instead of assuming a preset is close enough — a mismatched `GAP`/`SIZE` height is exactly what
causes one barcode's content to bleed across several physical labels (see below).

**Why GapYIn matters**: every preset carries a `GapYIn` — the physical gap/perforation between
labels along the feed. `SIZE`/`GAP` (TSPL) and `^LL`/label-length (ZPL) are always computed from
**one label's real height plus that gap**, never guessed or left at a printer's stale default —
this is what stops a single barcode/label's content from printing across several physical label
boundaries (the failure mode where one image spans 2-3 labels on the roll because the printer
thinks each "label" is taller than it physically is).

**DPI**: 203dpi is the common/standard resolution for desktop thermal printers and is sufficient
for standard 1D barcodes (EAN-13, Code128, GS1-128) and normal label text. 300dpi thermal
printers exist and are preferable when a label carries small text or very dense codes, but this
module hard-codes 203dpi in every `LabelTemplate` — there is no 300dpi preset today.

**Avery sheet** (`sheet` field, resolved by `AverySpecByName` in `label.go`) — unrelated to the
thermal templates above; this is a full A4/Letter page fed through an ordinary office printer:

| Preset | Paper | Grid | Label size | Region fit |
|---|---|---|---|---|
| `l7160` (default) | A4 | 3 cols × 7 rows = 21/sheet | 63.5×38.1mm | Most of the world outside North America (A4 is the standard office paper size there). |
| `5160` | US Letter | 3 cols × 10 rows = 30/sheet | 66.7×25.4mm (2-⅝"×1") | US/Canada (Letter is the standard office paper size there). Same physical grid shared by Avery 5160/8160/5260/8460 — those SKUs differ in adhesive/finish, not layout, which is why one `AverySpec` covers all four. |

## The rotation bug and how Rotate fixes it

Labels were printing **rotated 90°** on a real Xprinter XP-330B: the barcode bars printed, but
the human-readable text and overall layout ran sideways relative to the feed direction. Root
cause: `RenderSingleLabelPDF` used to hardcode every single-item label PDF to a fixed
50.8mm×25.4mm **landscape** page regardless of what roll/paper was actually loaded — the user's
physical roll turned out to be a single narrow lane feeding **long-edge-first** (portrait media),
the opposite of what the code assumed, so the PDF viewer/Windows driver rotated content to fit.
There was no stray `rotate()` call to remove; it was a page-dimension/orientation **mismatch**.

Every `LabelTemplate` now carries an explicit `Rotate bool`: "the physical media is mounted so
content must print turned 90° to read correctly along the feed direction." Each renderer
implements this the same concept differently, because each format's rotation primitive differs:

| Renderer | Physical media dims | Rotation mechanism |
|---|---|---|
| PDF (`RenderSingleLabelPDF`) | `Rotate=false`: page = template W×H. `Rotate=true`: page dims **swapped** to H×W (physically portrait). | `drawLabelCell` runs inside an `fpdf.TransformBegin/TransformRotate(90,…)/TransformEnd` block, centered on the page, so the same card layout fills the swapped page. |
| ZPL (`renderZPL`) | `^PW`/`^LL` = the roll **as mounted**, never swapped. | `Rotate=true` emits `^FWR` right after `^XA`/`^CI28` (previously never emitted anywhere in this module — the literal gap that let this bug through). |
| TSPL (`renderTSPL`) | `SIZE`/`GAP` = the roll **as mounted**, never swapped. | Each `TEXT`/`BARCODE` command's own rotation parameter is `90` instead of `0`. |

**Known limitation**: this fix can't stop an operator from picking a mismatched Windows-driver
paper-preset name in a print dialog. If you're printing via a PDF and the OS print dialog, pick
the driver's paper-preset that actually measures the same as your chosen `LabelTemplate` — or
better, skip the print dialog entirely and use [direct USB printing](#direct-usb-printing-via-the-local-print-agent),
which sends raw command bytes straight to the printer with no OS page/orientation negotiation at all.

**Bench-verified addendum (2026-08-02)**: printing a real label directly to an Xprinter XP-330B
(bypassing the OS print dialog entirely, via the local print-agent — see below) surfaced that the
"never swap SIZE/W×H, only toggle Rotate" rule above wasn't the whole story: the actual root cause
on the real roll tested was that a preset's declared W×H were simply **transposed relative to the
physical roll** (see library-api's `1row_29x62` / this file's `1row_29x62` preset — a genuinely
narrow-then-long roll had been modeled as wide-then-short). Getting the template's own W×H right
for the roll actually loaded is the first thing to check before reaching for `Rotate` — `Rotate`
solves "this template's proportions are right but the roll is mounted turned 90°," not "this
template's declared proportions are wrong for the roll." A second, separate issue also surfaced
on the bench-verified narrow/tall preset: content prints upside-down even with `Rotate=false`;
see `1row_29x62`'s "Known follow-up" note in library-api's `docs/barcode-labels.md` — two attempted
software fixes for that (`DIRECTION 1`, per-field `rotation=180`) both made physical alignment
worse, so it was left unfixed rather than risk a regression.

## TSPL support (Xprinter/TSC-compatible printers)

`renderTSPL` (`tspl.go`) emits real TSC/TSPL2 commands, confirmed against TSC's public
programming manual:

```
SIZE <w> mm,<h> mm     -- physical label size (one lane's width × height, or the full roll width for multi-lane)
GAP <gap> mm,0 mm      -- gap-fed (die-cut) stock: distance between labels along the feed
DIRECTION 0
REFERENCE 0,0
CLS                     -- clear the image buffer, once per feed-row
TEXT x,y,"3",rotation,x-mult,y-mult,"content"
BARCODE x,y,"128"|"EAN13",height,1,rotation,narrow,wide,"content"
PRINT 1,1
```

Symbology: `SymEAN13`/`SymUPCA` → `BARCODE ...,"EAN13",...` (12-digit body; the printer computes
the check digit, same convention `renderZPL` already uses for `^BE`). `SymCode128` → `"128"`.

**GS1-128 is explicitly unsupported for `thermal_tspl`.** The exact FNC1 escape convention inside
a TSPL `BARCODE` command's content string is a firmware detail that could not be confirmed
against Xprinter's specific TSPL clone from public sources — shipping it unverified risks
silently printing a barcode that scans as plain Code128 instead of GS1-128. `POST
/inventory/labels/print` rejects `format=thermal_tspl` combined with `include_lot`/
`include_serial` with `422 UNSUPPORTED_TSPL_GS1`. Use `thermal_zpl` or `avery_a4` for lot/serial
GS1-128 labels until this is bench-verified and implemented.

**Layout fitting (fixed 2026-08-02 via the same bench session)**: `writeTSPLLabel` used to size
title/SKU/detail text purely off the label's HEIGHT (`h/10/24`) and let the barcode fill roughly
half the remaining vertical space — fine for wide-but-short presets, but on a tall-but-narrow one
(`1row_29x62`) this let text overflow the printable width (10 characters at multiplier 2 is
~480 dots, double a 232-dot-wide label) and produced a barcode taller than it was wide (a dense
vertical column, not the normal short-and-wide look a Code128/EAN13 symbol should have). Fixed by:
sizing the font multiplier off `min(w,h)` instead of height alone (a tall-but-narrow label has
height to spare but almost no width to spare), switching from TSPL's larger built-in font "3"
(16×24 dots/char) to the smallest built-in font "1" (8×12 dots/char) — after fixing the width
overflow, text at font "3" still visually dominated a narrow label even once it technically fit,
per a follow-up bench print — then still clamping the multiplier down further until a
~12-character string would fit the label's width (truncating text to whatever actually fits at
the chosen multiplier); capping barcode height relative to width (scaled so wide presets like
`4x6` still get a taller, legible barcode); and centering both text and the barcode horizontally
using an estimated printed width (`estimateCode128WidthDots`/`estimateEAN13WidthDots` —
over-estimates for Code128 since Set-C digit-pairing can render narrower than the estimate, so
centering never pushes the real symbol past the label's edge). Also increased the top margin
(`h/10` instead of `h/20`) after a first-line title was observed getting clipped by the
operator's tear-off cut. Confirmed well-balanced (text size, centering, no clipping) against the
`1row_29x62` preset via a live bench print of this module's own renderer output.

## Direct USB printing via the local print-agent

Printing today (download a file → open in a viewer → manually pick a Windows paper preset) is
exactly the fragile path that produced the rotation bug above. `inventory-ui` can instead print
**directly via USB**, bypassing the OS print dialog entirely, by reusing `pos-service`'s existing
local "print-agent" — a small Windows-service companion the operator already runs on the till/
terminal (`pos-service/pos-api/cmd/print-agent`), listening on loopback `127.0.0.1:9330`:

- `GET /printers` — lists the printers installed in Windows (once the Xprinter's driver is
  installed, "Xprinter XP-330B" appears here by name).
- `POST /print` — `{"name": "<printer name>", "format": "rawhex", "data": "<hex-encoded bytes>"}`
  writes the bytes straight through the Windows spooler in **RAW datatype**
  (`alexbrainman/printer`'s `StartRawDocument`/`Write`), which bypasses GDI page-size/orientation
  negotiation entirely — the printer receives exactly the TSPL/ZPL bytes this module generated,
  with no OS-level transform in between.

**No changes were needed to the print-agent itself** — both routes already exist and are already
generic/CORS-open, not POS-specific. `inventory-ui`'s `lib/inventory/print-agent.ts` calls the
same already-running agent process; see `PrintLabelsDialog.tsx`'s "Print via Local Agent" action.
Operator prerequisite: the Xprinter's Windows driver must be installed (so the printer has a
name in `/printers`) and the print-agent service must be running.

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
| `format` | `avery_a4` \| `thermal_zpl` \| `thermal_tspl` \| `dymo` | Output format (PDF vs. printer command text). |
| `sheet` | `l7160` (default) \| `5160` | Avery grid preset, only used when `format: "avery_a4"`. |
| `template` | preset name (see the templates table above) \| `custom` | Physical label-roll template, used when `format` is `thermal_zpl` or `thermal_tspl`. `thermal_size` (old single-lane-only name) still works for back-compat. |
| `custom_label_w_in`/`custom_label_h_in`/`custom_lanes`/`custom_gap_x_in`/`custom_gap_y_in` | | Only used when `template: "custom"`. |
| `rotate` | bool | Overrides the resolved template's own `Rotate` default — see [§3](#the-rotation-bug-and-how-rotate-fixes-it). |
| `include_lot` | bool | One GS1-128 label per active lot of each selected item, embedding `(10)`/`(17)`. Not supported with `thermal_tspl` (see [§4](#tspl-support-xprintertsc-compatible-printers)). |
| `include_serial` | bool | One GS1-128 label per available serial, embedding `(21)`. Not supported with `thermal_tspl`. |
| `include_price` | bool | Adds `(392n)` to the GS1 payload and a printed price line. |
| `price_decimals`, `currency` | | Formatting for the printed price line and the `(392n)` implied-decimals value. |
| `category_id` / `supplier_id` / `purchase_order_id` / `item_ids` | | Selection — exactly one drives which items get labelled. |
| `qty_per_item`, `quantities` | | How many copies of each item's label to emit. |

A tenant can save a default template/format/rotate combination (`TenantInventoryConfig
.LabelPrintDefaults`, via `PUT /inventory/settings`) so callers that don't specify `template`
default to it instead of the hardcoded 4x2 fallback.

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

## Scope decision: no shared Go module (yet)

`library-service/library-api`'s barcode module (`card.go`/`label.go`/`sheet.go`) mirrors this
one's approach (boombuler/barcode + go-pdf/fpdf) but was, until this pass, Code128/PDF-only —
its own header comment recorded a deliberate choice to stay lean rather than share this module.
When direct-USB printing was extended to library-service too, library-api gained its **own
local** `template.go`/`tspl.go` (a smaller mirror of this module's — no GS1-128, no lot/serial,
since library's `CopyLabel` never needed them), not a shared `shared/*` Go package. Reasoning:

1. Extracting a shared package now would mean onboarding two already-deployed services to a
   brand-new pinned-tag dependency (see `shared/httpware`'s tag workflow) for what's still a
   small amount of genuinely-common code (Code128/EAN13 PNG rendering into an fpdf cell).
2. The two modules' actual needs still diverge — GS1-128, ZPL, avery multi-sheet lot/serial
   logic exist only here; library only ever needed Code128 + a fixed single-lane thermal size.
3. Both modules are written so a **future** extraction (if the divergence narrows) is a
   mechanical lift: `LabelTemplate`/`renderTSPL`/`renderZPL` only ever touch this module's own
   `Label`/`Symbology` types, nothing inventory-specific leaks into the rendering logic itself.

If a future change makes the two modules' needs converge further, revisit this decision —
don't silently re-litigate it without recording why.
