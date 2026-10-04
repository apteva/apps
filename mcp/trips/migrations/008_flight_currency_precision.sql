-- Older Duffel normalisation always used two decimals. Repair the scale of
-- saved deal observations only; user-entered itinerary costs already use ISO
-- minor units and must remain unchanged. Previously truncated sub-cent digits
-- in three/four-decimal currencies cannot be recovered from old observations.
UPDATE travel_price_observations SET amount_cents=amount_cents/100
WHERE provider='duffel' AND kind='flight' AND currency IN
('BIF','CLP','DJF','GNF','ISK','JPY','KMF','KRW','PYG','RWF','UGX','UYI','VND','VUV','XAF','XOF','XPF');
UPDATE travel_price_observations SET amount_cents=amount_cents*10
WHERE provider='duffel' AND kind='flight' AND currency IN
('BHD','IQD','JOD','KWD','LYD','OMR','TND')
  AND amount_cents <= 900719925474099;
UPDATE travel_price_observations SET amount_cents=amount_cents*100
WHERE provider='duffel' AND kind='flight' AND currency IN ('CLF','UYW')
  AND amount_cents <= 90071992547409;
