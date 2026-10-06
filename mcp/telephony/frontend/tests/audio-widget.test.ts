import { expect, test } from "bun:test";
import { audioWidgetProblems, audioWidgetMeasurements } from "../../ui/audio-widget";
import type { AudioReport } from "../../ui/audio-dashboard";
const report = {state:"ended",issues:["reconnect","dropped_audio","carrier_stall"],metrics:{playback_dropped_ms:1840,carrier_max_gap_ms:6800,carrier_source_dropped_ms:1200,reconnects:2}} as unknown as AudioReport;
test("ended calls retain visible problem severity", () => {
 const problems=audioWidgetProblems(report);expect(problems.map(x=>x.code)).toEqual(["dropped_audio","carrier_stall","reconnect"]);expect(problems[0].tone).toBe("error");expect(problems[2].tone).toBe("warning");
});
test("drop boundaries stay separate and values have honest units", () => {
 const values=audioWidgetMeasurements(report);expect(values).toHaveLength(4);expect(values[0].value).toBe("1.84 s");expect(values[1].value).toBe("6.8 s");expect(values[0].label).not.toContain("(ms)");expect(values.at(-1)?.value).toBe("2");
});
test("zero and invalid counters do not present fabricated errors", () => {
 expect(audioWidgetMeasurements({...report,metrics:{playback_dropped_ms:0,carrier_max_gap_ms:NaN,reconnects:-1}})).toEqual([]);expect(audioWidgetProblems({...report,issues:[]})).toEqual([]);
});
