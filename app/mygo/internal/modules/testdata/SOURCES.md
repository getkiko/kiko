# Module fixtures

Real `.sgmodule` files used by the golden tests (`golden_test.go`). They are
unmodified copies; the golden files in `golden/` are the reviewed conversion
results. Downloaded on 2026-11-04.

| File | Source |
| --- | --- |
| `youtube_enhance.sgmodule` | https://raw.githubusercontent.com/Maasea/sgmodule/master/YouTube.Enhance.sgmodule |
| `bilibili_helper.sgmodule` | https://raw.githubusercontent.com/Maasea/sgmodule/master/Bilibili.Helper.sgmodule |
| `keepads.sgmodule` | https://raw.githubusercontent.com/Maasea/sgmodule/master/KeepAds.sgmodule |
| `advertising_script_mock.sgmodule` | https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rewrite/Surge/AdvertisingScript/AdvertisingScript_Mock.sgmodule |
| `redirect.sgmodule` | https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rewrite/Surge/Redirect/Redirect.sgmodule |
| `block_httpdns.sgmodule` | https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rewrite/Surge/BlockHTTPDNS/BlockHTTPDNS.sgmodule |
| `get_cookie.sgmodule` | https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rewrite/Surge/GetCookie/GetCookie.sgmodule |
| `pinduoduo_body.sgmodule` | https://raw.githubusercontent.com/fmz200/wool_scripts/main/Surge/module/split/partP/Pinduoduo.sgmodule (a bare `http-response` Body Rewrite line) |

The scripts and mapping data the modules refer to are not downloaded: the
tests answer every `Fetches` URL with a small stand-in.
