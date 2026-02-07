from coinmark_api.ingest.buckets import floor_bucket_start_ms


def test_floor_15m() -> None:
    # 00:16:01 -> 00:15:00
    ts = (16 * 60 + 1) * 1000
    assert floor_bucket_start_ms(ts, "15m") == 15 * 60 * 1000


def test_floor_1h() -> None:
    ts = (3 * 3600 + 59) * 1000
    assert floor_bucket_start_ms(ts, "1h") == 3 * 3600 * 1000


def test_floor_4h() -> None:
    # 07:59 -> 04:00
    ts = (7 * 3600 + 59 * 60) * 1000
    assert floor_bucket_start_ms(ts, "4h") == 4 * 3600 * 1000
