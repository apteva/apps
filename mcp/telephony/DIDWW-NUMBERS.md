# DIDWW number search and ordering

Telephony's provider-neutral number tools now support DIDWW.

## Search

`telephony_numbers_search` resolves the ISO country through DIDWW, selects the
requested DID group type, and reads group coverage plus SKU pricing. If the
account has DIDWW's optional `/available_dids` permission, the result contains
individual E.164 numbers and an exact order resource. If that permission is
disabled, Telephony returns a clearly labelled group/SKU quote; DIDWW chooses a
number from that coverage when the order is submitted.

Group quotes do not invent a phone number. The UI displays the city/type and
states that the number is selected after ordering. Area-code and pattern filters
only apply to individual inventory; Telephony reports this limitation when an
account exposes group coverage only.

## Purchase and retries

The existing short-lived confirmation token and one-resource claim guard apply
to both exact and group quotes. DIDWW orders use `allow_back_ordering: false`.
An order is recorded as `pending` until DIDWW reports `completed`; retrying the
same token polls `get_order` and never creates a second order. Canceled orders
are terminal failures. Provider resource, group, SKU, inventory mode, and the
DIDWW order response are persisted for reconciliation.

No number is purchased automatically by search. A purchase still requires an
explicit `telephony_numbers_purchase` confirmation and available DIDWW credit.

## French registration

French DIDWW local groups report a registration requirement. Telephony exposes
the provider-neutral address tools plus `telephony_identities_list`,
`telephony_identity_create`, and `telephony_identity_get`; DIDWW addresses are
created against an identity. The compliance-profile tools list, inspect, and
create DIDWW address-verification tasks, and
`telephony_regulatory_requirements` can inspect either a filtered collection or
one requirement by ID. DIDWW's documented lifecycle creates
the order first. Once the order allocates a DID, create a business identity,
attach it to a French address, complete DIDWW's proof/requirement workflow, and
wait for an **Approved** verification before activating the number. Telephony
also exposes identities and address verifications through the existing generic
compliance-profile list/create/get/evaluate tools. A DIDWW profile create maps
to a personal or business identity; verification creation remains a separate
step because it needs the address and allocated DID. Telephony does not pretend
that an address or verification was attached to the order; the purchase result
marks registration as required and returns the next step.

The provider API may omit a currency on SKU resources; Telephony uses the
connection's `currency`/`pricing_currency` field when present and otherwise uses
DIDWW's documented USD account pricing.
