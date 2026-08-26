import { describe, expect, test } from "bun:test";
import { bookedCallsPreferences, upcomingBookedCalls } from "./BookedCallsWidget";

describe("BookedCallsWidget helpers", () => {
  test("normalizes per-widget settings", () => {
    expect(bookedCallsPreferences()).toEqual({ horizonDays: 30, maxItems: 6, bookingTypeId: 0 });
    expect(bookedCallsPreferences({ horizon_days: 900, max_items: -4, booking_type_id: 7.4 })).toEqual({
      horizonDays: 365,
      maxItems: 1,
      bookingTypeId: 7,
    });
  });

  test("keeps only active upcoming Calls bookings in chronological order", () => {
    const now = Date.parse("2026-08-26T10:00:00Z");
    const booking = (id: number, status: string, start_at: string, end_at: string, calls_room_id?: number) => ({
      id,
      booking_type_id: 1,
      status,
      start_at,
      end_at,
      invitee_name: "Guest",
      invitee_email: "guest@example.com",
      calls_room_id,
    });
    const result = upcomingBookedCalls([
      booking(2, "confirmed", "2026-08-26T12:00:00Z", "2026-08-26T12:30:00Z", 2),
      booking(1, "rescheduled", "2026-08-26T11:00:00Z", "2026-08-26T11:30:00Z", 1),
      booking(3, "cancelled", "2026-08-26T10:30:00Z", "2026-08-26T11:00:00Z", 3),
      booking(4, "confirmed", "2026-08-26T09:00:00Z", "2026-08-26T09:30:00Z", 4),
      booking(5, "confirmed", "2026-08-26T10:30:00Z", "2026-08-26T11:00:00Z"),
    ], now);
    expect(result.map(({ id }) => id)).toEqual([1, 2]);
  });
});
