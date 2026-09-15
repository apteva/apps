import { expect, test } from "bun:test";
import { decimalMinor, displayMinor, authorizationLink } from "./BankPayments";

test("payment amounts remain exact through entry and review", () => {
  expect(decimalMinor("12.34")).toBe(1234);
  expect(decimalMinor("0.01")).toBe(1);
  expect(decimalMinor("10000")).toBe(1000000);
  expect(displayMinor(decimalMinor("90071992547409.91"))).toBe("90071992547409.91");
  for (const value of ["0", "-1", "1.001", "1e3", "1,000", "Infinity", "90071992547409.92", ""]) expect(() => decimalMinor(value)).toThrow();
});

test("bank authorization links accept provider hosts and reject misleading URLs",()=>{
 for(const [provider,url] of [["enable-banking","https://auth.enablebanking.com/pis/start"],["truelayer-payments","https://payment.truelayer-sandbox.com/pay"],["saltedge-payments","https://www.saltedge.com/connect"],["plaid","https://secure.plaid.com/link"]])expect(authorizationLink(provider,url)).toBe(url);
 for(const url of ["javascript:alert(1)","http://secure.plaid.com/link","https://plaid.com.attacker.test/link","https://user:pass@secure.plaid.com/link"])expect(()=>authorizationLink("plaid",url)).toThrow();
});
