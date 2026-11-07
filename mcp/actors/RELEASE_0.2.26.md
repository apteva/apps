# Actors 0.2.26

Generic fields now support `own_text` for direct node text without nested badge labels. Short literal keyboard input is available through `type`, bounded to 256 bytes and excluded from read-only operations. Existing extraction and interaction defaults remain unchanged.

The editable AdultFolio example adds `search_exact_model` through the native autocomplete endpoint. It verifies account, exact name, unique model suggestion, selection identity, canonical profile and independent profile/message IDs. Search input focus is resolved through fresh SOM; the unsupported autocomplete option widget uses a verified unique semantic selector with exact name and navigation-effect guards. No coordinates, guessed profile URLs or HTTP-error response bodies are used.

Fixtures cover nested membership badges and names ending in badge-like words; wrong account, query, name, role, selection, profile ID, canonical URL and ambiguous suggestions fail closed. Live verification and publication are performed separately. No live message is sent by examples or fixtures.
