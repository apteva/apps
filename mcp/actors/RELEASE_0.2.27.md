# Actors 0.2.27

Corrects the editable AdultFolio exact-model lookup for live browser state: verifies the query through fresh SOM instead of an unchanged HTML value attribute, and returns the observed profile URL as `current_url` instead of requiring a head canonical tag absent from Computer extraction. Exact name, model role, unique suggestion, selection ID, independent profile/message ID, profile URL shape and unchanged account are checked before success. Search focus uses SOM and suggestion navigation uses a guarded unique semantic selector; no coordinates are used.

Fixtures cover empty HTML value attributes, nested arbitrary membership badges, incorrect live query, account/name/role/ID changes, ambiguous or missing selection and invalid profile routes. Existing reply guard and persisted-message verification tests also pass. This release changes only editable examples and verification fixtures; generic engine behavior is unchanged from 0.2.26. No live reply is sent by the tests.
