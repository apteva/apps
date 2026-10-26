# media-studio

## Brand production profiles

Supply both `brand_id` and `production_profile` to `media_generate` to resolve a named production profile from the optionally bound Editorial app. Profiles can require an asset kind and provider, plus an approved Persona Studio identity/style. HeyGen profiles require `kind=avatar`; conflicting generic video, provider, avatar and voice requests fail before dispatch. Bind Editorial and Persona Studio to use this path. Existing unscoped calls retain their behavior. Cache reuse is namespaced by the resolved policy and identity.
