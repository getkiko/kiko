# Device image sources

Retrieved on 2026-11-05.

## Apple device images

Source: <https://github.com/littlebyteorg/apple-device-images> (the appledb device image library); the 256px WebP files come from `https://img.appledb.dev/device@256/<deviceKey>/<Color>.webp`.
License: the images are Apple product images, copyright Apple Inc. They are used here only as small icons identifying the device type, as the repository distributes them publicly, without modification.

| Key | File | Source URL |
| --- | --- | --- |
| iphone | img/iphone.webp | https://img.appledb.dev/device@256/iPhone16,1/Natural%20Titanium.webp |
| ipad | img/ipad.webp, img/ipad_dark.webp | https://img.appledb.dev/device@256/iPad14,5/Silver.webp and Silver_dark.webp |
| watch | img/watch.webp | https://img.appledb.dev/device@256/Watch7,1/0.webp |
| mac | img/mac.webp, img/mac_dark.webp | https://img.appledb.dev/device@256/MacBookPro18,1/Silver.webp and Silver_dark.webp |
| appletv | img/appletv.webp | https://img.appledb.dev/device@256/AppleTV14,1/Black.webp |
| homepod | img/homepod.webp | https://img.appledb.dev/device@256/AudioAccessory6,1/Midnight.webp |
| airpods | img/airpods.webp | https://img.appledb.dev/device@256/AirPodsPro1,2-case/White.webp |
| vision | img/vision.webp | https://img.appledb.dev/device@256/RealityDevice14,1/White.webp |

The repository provides `_dark` variants for some devices only; the other keys use the same image in dark mode.

## Original images

`img/pc.png` (Windows / Linux computer) and `img/router.png` (router) are drawn by `gen/main.go` with the Go standard library (signed distance fields, analytic anti-aliasing, gradients and soft shadows). They are original to this project and covered by its license.
