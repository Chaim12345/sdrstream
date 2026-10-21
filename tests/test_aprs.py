"""Tests for the APRS-IS packet parsing (no network)."""

from __future__ import annotations

import unittest

from sdrstream import aprs


class ParsePacketTest(unittest.TestCase):
    def test_comment_lines_are_skipped(self) -> None:
        self.assertIsNone(aprs.parse_packet("# aprsc 2.1.23-gb95bf1f"))
        self.assertIsNone(aprs.parse_packet(""))
        self.assertIsNone(aprs.parse_packet("no arrow here"))

    def test_position_packet(self) -> None:
        line = ("4X1UN>APRS,TCPIP*:!3247.34N/03500.22E#PHG5730 "
                "Haifa area")
        packet = aprs.parse_packet(line)
        self.assertIsNotNone(packet)
        self.assertEqual(packet.source, "4X1UN")
        self.assertEqual(packet.destination, "APRS")
        self.assertEqual(packet.kind, "position")
        self.assertAlmostEqual(packet.latitude, 32.79, places=2)
        self.assertAlmostEqual(packet.longitude, 35.0, places=2)
        self.assertIn("Haifa", packet.comment)

    def test_southern_and_western_hemispheres(self) -> None:
        line = "CALL-9>APRS,TCPIP*:!3247.34S/03500.22W#somewhere else"
        packet = aprs.parse_packet(line)
        self.assertAlmostEqual(packet.latitude, -32.79, places=2)
        self.assertAlmostEqual(packet.longitude, -35.0, places=2)

    def test_weather_packet(self) -> None:
        line = ("4X1UN-1>APRS,TCPIP*:!3247.34N/03500.22E_220/004g005"
                "t012r000p000P000h89b1013")
        packet = aprs.parse_packet(line)
        self.assertEqual(packet.kind, "weather")
        self.assertIn("t012", packet.comment)

    def test_message_packet(self) -> None:
        line = "W1AW>APRS,TCPIP*:>4X1UN hello from the network"
        packet = aprs.parse_packet(line)
        self.assertEqual(packet.kind, "status")
        self.assertEqual(packet.source, "W1AW")

    def test_telemetry_packet(self) -> None:
        line = "4X1UN-2>APRS,TCPIP*:T#5,199,000,255,073,123,01101001"
        packet = aprs.parse_packet(line)
        self.assertEqual(packet.kind, "telemetry")

    def test_summary_shape(self) -> None:
        line = "4X1UN>APRS,TCPIP*:!3247.34N/03500.22E#Haifa area"
        packet = aprs.parse_packet(line)
        text = packet.summary()
        self.assertIn("4X1UN", text)
        self.assertIn("position", text)
        self.assertIn("32.7", text)


class LoginLineTest(unittest.TestCase):
    def test_plain_login(self) -> None:
        line = aprs.login_line("N0CALL")
        self.assertTrue(line.startswith("# login N0CALL pass -1"))
        self.assertTrue(line.endswith("\r\n"))

    def test_filtered_login(self) -> None:
        line = aprs.login_line("4X1UN", "r/32.79/34.99/100")
        self.assertIn("filter r/32.79/34.99/100", line)


if __name__ == "__main__":
    unittest.main()
