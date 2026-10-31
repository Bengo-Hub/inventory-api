# Catalog tax codes

Every inventory item carries a treasury tax code (`items.tax_code_id`). treasury-api owns the codes
and their rates; inventory stores only the code. Codes are matched by their KRA band
(`tax_codes.kra_code`), because the spelling differs per tenant: one tenant has its own `B`, another
uses the platform-global `VAT-16`, and both are band B.

## The automatic code

An item created without a code, and any item whose code is empty, gets the tenant's automatic code:

1. the default chosen in Settings, Tax & Compliance (`tenant_inventory_configs.default_tax_code`), when one is set;
2. otherwise band **D (Non-VAT)**, while treasury reports the business is not charging VAT;
3. otherwise band **B (VAT 16%)**, once the business is VAT-registered and charging VAT
   (treasury `tenant_tax_profiles.vat_registered` and `auto_charge_vat`, the same switch POS uses).

When a tenant has its own code for a band it wins over the platform-global one; `is_default` breaks ties.

## Who chose the code

`items.metadata.tax_code_source` records it:

| Value | Set when |
|---|---|
| `manual` | A person picked the code: the item form (a code different from the stored one), the bulk "set tax code" action, or an import row with a `tax_code_id`. |
| `auto` or absent | The system set it: the automatic code on create, a background job, a variant copying an automatic parent code. |

`UpdateItem` replaces metadata as a whole, so the stored marker is carried across every update and a
client cannot set or drop it by resending metadata. Variants inherit their parent's marker.

## Background jobs

All three run through the shared `bulkjobs` service (job type `item_tax_codes`), so each is a
persisted job row and finishes with a `bulk_job.completed` push. Items are updated 250 per
transaction, in id order, each with its `inventory.item.updated` event in the same transaction, so
POS, ordering and treasury's eTIMS item mirror follow. Nothing here calls KRA; an item's eTIMS
registration changes only on an explicit sync from treasury.

| Trigger | Mode | What changes |
|---|---|---|
| Daily, and 2 minutes after startup (one replica, `inventory:tax-code-reconcile` claim) | `reconcile` | Items with no code get the automatic code. While VAT-active, automatic band D items move to band B. |
| `treasury.tax.vat_status_changed` with `vat_active=true` | `reconcile` | Same as above, straight away. A Settings default in band D is cleared back to automatic first. |
| `treasury.tax.vat_status_changed` with `vat_active=false` | `vat_off` | Automatic band B items move to band D. A Settings default in band B is cleared back to automatic first. |
| A new default saved in Settings | `override` | Every item gets the new code (or the automatic code when set back to Automatic), hand-set items included; markers reset to `auto`. The UI asks for confirmation first. |

Hand-set codes are never moved by `reconcile` or `vat_off`. A Settings default in another band
(zero-rated C, exempt A, 8% E) is a deliberate choice and is kept when VAT is switched.

If treasury cannot be reached, a job changes nothing (`ErrTaxCodesUnavailable`): the VAT event is
retried, the reconcile tries again the next day. The strict `treasury.Client.VATStatus` is used for
this instead of `VATActive`, which answers "charge VAT" on error for pricing safety.

## Events

- Consumed: `treasury.tax.vat_status_changed` (`tenant_id`, `vat_active`), durable
  `inventory-treasury-vat-status`. Published by treasury-api only when a tax profile write actually
  flips VAT charging (`UpdateTaxProfile`, `SyncObligations`).
- Consumed: `treasury.tax.code_updated`, durable `inventory-treasury-tax-cache-invalidate` (cache only).
- Published: `inventory.item.updated` per changed item.

## Settings API

`GET|PUT /{tenant}/inventory/settings` also returns `automatic_tax_code` (what Automatic resolves to
now) and `vat_active`. A PUT that changes `default_tax_code` validates it against the tenant's
treasury codes (422 `invalid_tax_code` otherwise) and returns `tax_code_job` (`job_id`, `code`,
`items`). An empty `default_tax_code` means Automatic.

## Catalog prices and VAT

The catalog's net and VAT split of a price (`net_price`, `tax_amount`, `tax_rate`) comes from the
platform's shared calculator (`github.com/Bengo-Hub/httpware/tax`, through `items.ComputeTaxSplit`),
the same one POS, ordering and treasury use, so a price splits to the same cent everywhere. The rate
comes from treasury for the item's own code, else the tenant default. When no rate is known (no code,
or treasury unreachable with nothing cached) the item carries no VAT split; a rate is never assumed.

Purchase orders carry no VAT of their own. The purchase order PDF prints the difference between the
lines and the total as "Shipping and charges". Input VAT is booked from the supplier's invoice.

## Item form

The tax fields (tax code, inclusive pricing, eTIMS classification, packaging and quantity units)
show for every item type. They used to be hidden for ingredients and equipment, which eTIMS still
registers and bands on purchases and stock movements.
