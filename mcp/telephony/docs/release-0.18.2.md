# Telephony 0.18.2

- Due routing decisions start independently of older carrier and AI fallback
  work. An eligible adviser can receive an offer while another call's fallback
  waits on the carrier.
- Routing function requests use the remaining decision budget. The call must
  still be pending and within its deadline when Telephony commits an offer.
- Routing logs link the function response, call lock, offer creation, and
  decision commit by call and decision ID.
