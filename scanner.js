const Scanner = (() => {
  let isScanning = false;
  let tesseractWorker = null;
  let scanTimeout = null;
  let lastOcrTime = 0;
  let vinDetected = false;
  let detectionFrame = null;
  let quaggaActive = false;
  let nativeCameraActive = false;
  let zxingReader = null;
  const OCR_INTERVAL = 800;
  const BARCODE_INTERVAL = 300;
  const OCR_INTERVAL_IOS = 400;
  const DEBUG = true;

  function isMobileOrTablet() {
    const ua = navigator.userAgent;
    const isPhone = /Android|webOS|iPhone|iPod|BlackBerry|IEMobile|Opera Mini/i.test(ua);
    const isTablet = /iPad/i.test(ua) || (/Android/i.test(ua) && !/Android.*Mobile/i.test(ua));
    const isDesktopBrowser = /Windows|Win64|Macintosh|Linux|(?:Chrome|Firefox|Safari|Edge|Opera).*Version/i.test(ua) && !/Mobile/i.test(ua);
    if (isDesktopBrowser) return false;
    return isPhone || isTablet;
  }

  function isIOS() {
    return /iPhone|iPad|iPod/i.test(navigator.userAgent);
  }

  function showScanButton() {
    const btn = document.getElementById('scanBtn');
    if (btn && isMobileOrTablet() && hasCameraSupport()) {
      btn.classList.add('mobile-visible');
    }
  }

  function hasCameraSupport() {
    return !!(
      navigator.mediaDevices?.getUserMedia ||
      navigator.getUserMedia ||
      navigator.webkitGetUserMedia ||
      navigator.mozGetUserMedia ||
      navigator.msGetUserMedia
    );
  }

  function loadWebRTCAdapter() {
    return new Promise((resolve, reject) => {
      if (window.adapter) {
        resolve();
        return;
      }
      const script = document.createElement('script');
      script.src = 'https://webrtc.github.io/adapter/adapter-latest.js';
      script.onload = resolve;
      script.onerror = () => reject(new Error('Failed to load WebRTC adapter'));
      document.head.appendChild(script);
    });
  }

  function getUserMedia(constraints) {
    if (navigator.mediaDevices?.getUserMedia) {
      return navigator.mediaDevices.getUserMedia(constraints);
    }
    const fn = navigator.getUserMedia ||
                navigator.webkitGetUserMedia ||
                navigator.mozGetUserMedia ||
                navigator.msGetUserMedia;
    if (fn) {
      return new Promise((resolve, reject) => {
        fn.call(navigator, constraints, resolve, reject);
      });
    }
    throw new Error('Camera not supported');
  }

  function loadQuaggaJS() {
    return new Promise((resolve, reject) => {
      if (window.Quagga) {
        resolve();
        return;
      }
      const script = document.createElement('script');
      script.src = 'https://cdn.jsdelivr.net/npm/@ericblade/quagga2@1.8.4/dist/quagga.min.js';
      script.onload = resolve;
      script.onerror = () => reject(new Error('Failed to load barcode scanner'));
      document.head.appendChild(script);
    });
  }

  function loadZXing() {
    return new Promise((resolve, reject) => {
      if (window.ZXing) {
        resolve();
        return;
      }
      const script = document.createElement('script');
      script.src = 'https://cdn.jsdelivr.net/npm/@zxing/library@0.21.3/umd/index.min.js';
      script.onload = resolve;
      script.onerror = () => reject(new Error('Failed to load ZXing barcode scanner'));
      document.head.appendChild(script);
    });
  }

  function loadTesseract() {
    return new Promise((resolve, reject) => {
      if (window.Tesseract) {
        resolve();
        return;
      }
      const script = document.createElement('script');
      script.src = 'https://cdn.jsdelivr.net/npm/tesseract.js@5/dist/tesseract.min.js';
      script.onload = resolve;
      script.onerror = () => reject(new Error('Failed to load OCR'));
      document.head.appendChild(script);
    });
  }

  function hasNativeBarcodeDetector() {
    return 'BarcodeDetector' in window;
  }

  async function initCamera(status) {
    const video = document.getElementById('scanViewport');
    
    if (!hasCameraSupport()) {
      if (DEBUG) console.error('[Camera] Camera API not supported');
      return { success: false, error: 'not-supported' };
    }
    
    const isIos = isIOS();
    if (DEBUG) console.log('[Camera] iOS detected:', isIos);
    
    const constraints = {
      video: {
        facingMode: 'environment',
        width: { ideal: 1280 },
        height: { ideal: 720 }
      },
      audio: false
    };
    
    try {
      if (DEBUG) console.log('[Camera] Requesting camera access with constraints:', constraints);
      
      const stream = await getUserMedia(constraints);
      
      video.setAttribute('playsinline', '');
      video.setAttribute('webkit-playsinline', '');
      video.muted = true;
      
      video.srcObject = stream;
      
      try {
        await video.play();
        if (DEBUG) console.log('[Camera] Video playback started');
      } catch (playErr) {
        if (DEBUG) console.error('[Camera] Video play failed:', playErr.name, playErr.message);
        if (playErr.name === 'NotAllowedError') {
          return { success: false, error: 'autoplay-blocked' };
        }
      }
      
      await new Promise((resolve) => {
        if (video.readyState >= 2) {
          resolve();
        } else {
          video.onloadedmetadata = resolve;
          setTimeout(resolve, 3000);
        }
      });
      
      nativeCameraActive = true;
      if (DEBUG) console.log('[Camera] Camera initialized. readyState:', video.readyState, 'videoWidth:', video.videoWidth);
      
      await new Promise(resolve => {
        const checkDimensions = () => {
          if (video.videoWidth > 0 && video.videoHeight > 0) {
            if (DEBUG) console.log('[Camera] Video dimensions ready:', video.videoWidth, 'x', video.videoHeight);
            resolve();
          } else if (video.readyState >= 2) {
            setTimeout(checkDimensions, 100);
          } else {
            setTimeout(resolve, 1000);
          }
        };
        checkDimensions();
      });
      
      return { success: true };
    } catch (err) {
      if (DEBUG) console.error('[Camera] Init failed:', err.name, err.message);
      
      if (err.name === 'NotAllowedError' || err.name === 'PermissionDeniedError') {
        if (isIos) {
          return { success: false, error: 'permission-denied-ios' };
        }
        return { success: false, error: 'permission-denied' };
      }
      if (err.name === 'NotReadableError' || err.name === 'TrackStartError') {
        return { success: false, error: 'camera-in-use' };
      }
      if (err.name === 'NotFoundError' || err.name === 'DevicesNotFoundError') {
        return { success: false, error: 'not-found' };
      }
      if (err.name === 'OverconstrainedError') {
        if (DEBUG) console.log('[Camera] Constraints too strict, retrying with basic constraints');
        return await initCameraFallback(status);
      }
      return { success: false, error: 'unknown', details: err };
    }
  }

  async function initCameraFallback(status) {
    const video = document.getElementById('scanViewport');
    
    try {
      if (DEBUG) console.log('[Camera] Trying fallback constraints...');
      const stream = await getUserMedia({ video: { facingMode: 'environment' }, audio: false });
      
      video.srcObject = stream;
      
      try {
        await video.play();
      } catch (playErr) {
        if (playErr.name === 'NotAllowedError') {
          return { success: false, error: 'autoplay-blocked' };
        }
      }
      
      await new Promise((resolve) => {
        if (video.readyState >= 2) {
          resolve();
        } else {
          video.onloadedmetadata = resolve;
          setTimeout(resolve, 3000);
        }
      });
      
      nativeCameraActive = true;
      if (DEBUG) console.log('[Camera] Fallback camera initialized successfully');
      return { success: true };
    } catch (err) {
      if (DEBUG) console.error('[Camera] Fallback also failed:', err.name, err.message);
      return { success: false, error: 'camera-failed', details: err };
    }
  }

  function stopCamera() {
    const video = document.getElementById('scanViewport');
    if (video && video.srcObject) {
      const tracks = video.srcObject.getTracks();
      if (DEBUG) console.log('[Camera] Stopping', tracks.length, 'tracks');
      tracks.forEach(track => {
        try {
          track.stop();
        } catch (e) {
          if (DEBUG) console.error('[Camera] Error stopping track:', e);
        }
      });
      video.srcObject = null;
      video.load();
      nativeCameraActive = false;
      if (DEBUG) console.log('[Camera] Stopped');
    }
  }

  async function initNativeBarcodeDetector() {
    if (!hasNativeBarcodeDetector()) {
      if (DEBUG) console.log('[Barcode] Native BarcodeDetector not supported');
      return null;
    }

    try {
      // Check which formats are actually supported on this device
      const allFormats = ['code_39', 'code_128', 'data_matrix', 'qr_code'];
      let supportedFormats = allFormats;

      if (BarcodeDetector.getSupportedFormats) {
        const available = await BarcodeDetector.getSupportedFormats();
        supportedFormats = allFormats.filter(f => available.includes(f));
        if (DEBUG) console.log('[Barcode] Supported formats:', supportedFormats, '(available:', available.join(', '), ')');
      }

      if (supportedFormats.length === 0) {
        if (DEBUG) console.log('[Barcode] No VIN-relevant barcode formats supported');
        return null;
      }

      const detector = new BarcodeDetector({ formats: supportedFormats });
      if (DEBUG) console.log('[Barcode] Native BarcodeDetector initialized with:', supportedFormats);
      return detector;
    } catch (e) {
      if (DEBUG) console.error('[Barcode] Native BarcodeDetector init failed:', e);
      return null;
    }
  }

  async function runNativeBarcodeDetection(detector, video, onDetected) {
    if (!detector || vinDetected || !nativeCameraActive) return;
    
    try {
      const barcodes = await detector.detect(video);
      if (barcodes.length > 0 && !vinDetected) {
        const code = barcodes[0].rawValue;
        if (DEBUG) console.log('[Barcode] Native detected:', code);
        const vin = validateVIN(code);
        if (vin) {
          onDetected(vin);
        }
      }
    } catch (e) {
      if (DEBUG) console.error('[Barcode] Native detection error:', e);
    }
  }

  async function initQuaggaFallback(status, onDetected) {
    if (hasNativeBarcodeDetector()) {
      if (DEBUG) console.log('[Fallback] Skipping Quagga, native BarcodeDetector supported');
      return;
    }

    if (quaggaActive) {
      if (DEBUG) console.log('[Fallback] Quagga already active');
      return;
    }

    if (DEBUG) console.log('[Fallback] Loading QuaggaJS...');
    
    try {
      await loadQuaggaJS();
      
      if (DEBUG) console.log('[Fallback] QuaggaJS loaded, using frame-decode mode');
      quaggaActive = true;

    } catch (e) {
      if (DEBUG) console.error('[Fallback] Quagga load failed:', e);
      if (status) {
        status.textContent = 'Barcode detection unavailable. Using text detection only.';
      }
    }
  }

  async function runQuaggaFrameDetection(onDetected) {
    if (!quaggaActive || !window.Quagga || vinDetected) return;

    const video = document.getElementById('scanViewport');
    if (!video || video.readyState < 2 || !video.videoWidth) return;

    try {
      const canvas = document.createElement('canvas');
      canvas.width = video.videoWidth;
      canvas.height = video.videoHeight;
      const ctx = canvas.getContext('2d');
      ctx.drawImage(video, 0, 0, canvas.width, canvas.height);

      const imageDataURL = canvas.toDataURL('image/png');

      Quagga.decodeSingle({
        src: imageDataURL,
        numOfWorkers: 0,
        locator: {
          patchSize: 'medium',
          halfSample: true
        },
        decoder: {
          readers: [
            'code_39_reader',
            'code_128_reader'
          ]
        },
        locate: true
      }, (result) => {
        if (result && result.codeResult) {
          const code = result.codeResult.code;
          if (DEBUG) console.log('[Fallback] Quagga frame detected:', code);
          const vin = validateVIN(code);
          if (vin) {
            onDetected(vin);
          }
        }
      });
    } catch (e) {
      if (DEBUG) console.error('[Fallback] Quagga frame decode error:', e);
    }
  }

  function stopQuagga() {
    if (quaggaActive) {
      quaggaActive = false;
      if (DEBUG) console.log('[Fallback] Quagga stopped');
    }
  }

  async function initZXing(status) {
    if (hasNativeBarcodeDetector()) {
      if (DEBUG) console.log('[ZXing] Skipping, native BarcodeDetector supported');
      return false;
    }

    if (zxingReader) {
      if (DEBUG) console.log('[ZXing] Already initialized');
      return true;
    }

    if (DEBUG) console.log('[ZXing] Loading ZXing library...');

    try {
      await loadZXing();

      const hints = new Map();
      const formats = [
        ZXing.BarcodeFormat.CODE_39,
        ZXing.BarcodeFormat.CODE_128,
        ZXing.BarcodeFormat.DATA_MATRIX,
        ZXing.BarcodeFormat.QR_CODE
      ];
      hints.set(ZXing.DecodeHintType.POSSIBLE_FORMATS, formats);
      hints.set(ZXing.DecodeHintType.TRY_HARDER, true);

      zxingReader = new ZXing.BrowserMultiFormatReader(hints);
      if (DEBUG) console.log('[ZXing] Initialized with Code 39, Code 128, Data Matrix, QR');
      return true;
    } catch (e) {
      if (DEBUG) console.error('[ZXing] Init failed:', e);
      zxingReader = null;
      return false;
    }
  }

  async function runZXingFrameDetection(onDetected) {
    if (!zxingReader || vinDetected) return;

    const video = document.getElementById('scanViewport');
    if (!video || video.readyState < 2 || !video.videoWidth) return;

    try {
      // Create a temporary image element from the current video frame
      const canvas = document.createElement('canvas');
      canvas.width = video.videoWidth;
      canvas.height = video.videoHeight;
      const ctx = canvas.getContext('2d');
      ctx.drawImage(video, 0, 0, canvas.width, canvas.height);

      const dataUrl = canvas.toDataURL('image/jpeg', 0.8);
      const result = await zxingReader.decodeFromImageUrl(dataUrl);

      if (result) {
        const code = result.getText();
        if (DEBUG) console.log('[ZXing] Detected:', code);
        const vin = validateVIN(code);
        if (vin) {
          onDetected(vin);
        }
      }
    } catch (e) {
      // NotFoundException is expected when no barcode is in frame - ignore silently
      if (e.name !== 'NotFoundException' && 
          e.constructor?.name !== 'NotFoundException' &&
          !(e.message && e.message.includes('No MultiFormat Readers'))) {
        if (DEBUG) console.error('[ZXing] Frame decode error:', e);
      }
    }
  }

  function stopZXing() {
    if (zxingReader) {
      try {
        zxingReader.reset();
      } catch (e) {}
      zxingReader = null;
      if (DEBUG) console.log('[ZXing] Stopped');
    }
  }

  async function initTesseractWorker() {
    if (tesseractWorker) return tesseractWorker;
    
    if (DEBUG) console.log('[OCR] Initializing Tesseract worker...');
    
    try {
      tesseractWorker = await Tesseract.createWorker('eng', 1, { 
        logger: m => DEBUG && console.log('[OCR Tesseract]', m.status, m.progress?.toFixed(2) || '')
      });
      
      // Whitelist VIN-valid characters and set single line mode
      await tesseractWorker.setParameters({
        tessedit_char_whitelist: 'ABCDEFGHJKLMNPRSTUVWXYZ0123456789',
        tessedit_pageseg_mode: '7'  // PSM.SINGLE_LINE - treat image as a single text line
      });
      
      if (DEBUG) console.log('[OCR] Worker initialized with VIN character whitelist');
      return tesseractWorker;
    } catch (e) {
      if (DEBUG) console.error('[OCR] Worker init failed:', e);
      throw e;
    }
  }

  async function terminateTesseractWorker() {
    if (tesseractWorker) {
      try {
        await tesseractWorker.terminate();
      } catch (e) {}
      tesseractWorker = null;
    }
  }

  function preprocessImageForOCR(sourceCanvas) {
    const canvas = document.createElement('canvas');
    canvas.width = sourceCanvas.width;
    canvas.height = sourceCanvas.height;
    const ctx = canvas.getContext('2d');
    
    ctx.drawImage(sourceCanvas, 0, 0);
    
    const imageData = ctx.getImageData(0, 0, canvas.width, canvas.height);
    const data = imageData.data;
    const w = canvas.width;
    const h = canvas.height;
    
    // Step 1: Convert to grayscale
    const gray = new Float32Array(w * h);
    for (let i = 0; i < gray.length; i++) {
      const idx = i * 4;
      gray[i] = 0.299 * data[idx] + 0.587 * data[idx + 1] + 0.114 * data[idx + 2];
    }

    // Step 2: Sharpen with 3x3 unsharp mask
    const sharpened = new Float32Array(w * h);
    const sharpenAmount = 1.5;
    for (let y = 1; y < h - 1; y++) {
      for (let x = 1; x < w - 1; x++) {
        const idx = y * w + x;
        const blur = (
          gray[(y-1)*w + (x-1)] + gray[(y-1)*w + x] * 2 + gray[(y-1)*w + (x+1)] +
          gray[y*w + (x-1)] * 2 + gray[y*w + x] * 4 + gray[y*w + (x+1)] * 2 +
          gray[(y+1)*w + (x-1)] + gray[(y+1)*w + x] * 2 + gray[(y+1)*w + (x+1)]
        ) / 16;
        sharpened[idx] = gray[idx] + sharpenAmount * (gray[idx] - blur);
      }
    }

    // Step 3: High contrast + adaptive binary threshold
    const contrast = 1.5;
    const factor = (259 * (contrast * 255 + 255)) / (255 * (259 - contrast * 255));
    
    for (let i = 0; i < sharpened.length; i++) {
      const val = Math.min(255, Math.max(0, sharpened[i]));
      const enhanced = Math.min(255, Math.max(0, factor * (val - 128) + 128));
      const binary = enhanced > 140 ? 255 : 0;
      
      const idx = i * 4;
      data[idx] = binary;
      data[idx + 1] = binary;
      data[idx + 2] = binary;
    }
    
    ctx.putImageData(imageData, 0, 0);
    return canvas;
  }

  function extractVINFromText(text) {
    const vinPattern = /[A-HJ-NPR-Z0-9]{17}/gi;
    const matches = text.match(vinPattern);
    if (matches) {
      for (const match of matches) {
        const cleaned = match.toUpperCase();
        if (/[IOQ]/.test(cleaned)) continue;
        if (validateVIN(cleaned)) {
          if (DEBUG) console.log('[OCR] Found VIN:', cleaned);
          return cleaned;
        }
      }
    }
    return null;
  }

  function validateVIN(vin) {
    if (!vin || typeof vin !== 'string') return false;
    const cleaned = vin.toUpperCase().replace(/[^A-HJ-NPR-Z0-9]/g, '');
    if (cleaned.length !== 17) return false;
    if (/[IOQ]/.test(cleaned)) return false;
    return cleaned;
  }

  function captureFrame() {
    const video = document.getElementById('scanViewport');
    if (!video) return null;
    
    if (video.readyState < 2) {
      if (DEBUG) console.log('[OCR] Video not ready, readyState:', video.readyState);
      return null;
    }

    const canvas = document.createElement('canvas');
    
    // Crop to center horizontal band (where the scan guide is)
    // VIN plates are typically in the center ~60% width, ~30% height band
    const sx = video.videoWidth * 0.1;
    const sy = video.videoHeight * 0.3;
    const sw = video.videoWidth * 0.8;
    const sh = video.videoHeight * 0.4;
    
    canvas.width = 1280;
    canvas.height = Math.round(1280 * (sh / sw));
    
    const ctx = canvas.getContext('2d');
    ctx.drawImage(video, sx, sy, sw, sh, 0, 0, canvas.width, canvas.height);
    
    if (DEBUG) console.log('[OCR] Frame captured, size:', canvas.width, 'x', canvas.height);
    
    return canvas;
  }

  async function performOCR(video, status, onDetected) {
    if (vinDetected) return;

    if (DEBUG) console.log('[OCR] Processing frame...');
    
    try {
      const worker = await initTesseractWorker();
      const canvas = captureFrame();
      if (!canvas) return;
      
      const preprocessedCanvas = preprocessImageForOCR(canvas);
      
      if (status) {
        status.textContent = 'Recognizing text...';
        status.className = 'scan-status processing';
      }
      
      if (DEBUG) console.log('[OCR] Recognizing...');
      const result = await worker.recognize(preprocessedCanvas);
      const text = result?.data?.text || '';
      
      if (DEBUG) console.log('[OCR] Raw text:', text.substring(0, 200));
      
      const vin = extractVINFromText(text);
      if (vin) {
        onDetected(vin);
      }
    } catch (e) {
      if (DEBUG) console.error('[OCR] Recognition error:', e);
      if (status) {
        status.textContent = 'OCR failed. Try manual entry.';
        status.className = 'scan-status error';
      }
    }
  }

  async function startDetectionLoop(status, onDetected) {
    const video = document.getElementById('scanViewport');
    const nativeDetector = await initNativeBarcodeDetector();
    const isIos = isIOS();
    const hasNativeBarcode = nativeDetector && nativeCameraActive;
    const ocrInterval = isIos ? OCR_INTERVAL_IOS : OCR_INTERVAL;
    
    if (hasNativeBarcode) {
      if (DEBUG) console.log('[Scanner] Using native BarcodeDetector + OCR');
      if (status) status.textContent = 'Scanning for VIN...';
    } else {
      if (DEBUG) console.log('[Scanner] No native BarcodeDetector - using ZXing + Quagga fallback + OCR');
      if (status) status.textContent = 'Scanning for VIN...';
      // Load ZXing as primary barcode fallback, Quagga as secondary
      initZXing(status).then(zxingOk => {
        if (!zxingOk && !vinDetected) {
          if (DEBUG) console.log('[Scanner] ZXing unavailable, trying Quagga');
          initQuaggaFallback(status, onDetected);
        }
      });
    }

    detectionFrame = setInterval(async () => {
      if (!video) {
        if (DEBUG) console.log('[Scanner] No video element');
        return;
      }
      
      if (DEBUG && lastOcrTime === 0) {
        console.log('[Scanner] First check - readyState:', video.readyState, 'videoWidth:', video.videoWidth, 'videoHeight:', video.videoHeight);
      }
      
      if (video.readyState < 2 || !video.videoWidth || !video.videoHeight) {
        if (DEBUG) console.log('[Scanner] Video not ready - readyState:', video.readyState, 'dimensions:', video.videoWidth, 'x', video.videoHeight);
        return;
      }
      
      if (vinDetected) return;
      
      if (hasNativeBarcode) {
        await runNativeBarcodeDetection(nativeDetector, video, onDetected);
      }

      if (!vinDetected && !hasNativeBarcode) {
        if (zxingReader) {
          await runZXingFrameDetection(onDetected);
        } else if (quaggaActive) {
          runQuaggaFrameDetection(onDetected);
        }
      }
      
      if (!vinDetected && nativeCameraActive) {
        const now = Date.now();
        const minInterval = lastOcrTime === 0 ? 0 : (isIos ? 300 : ocrInterval);
        if (now - lastOcrTime < minInterval) {
          return;
        }
        lastOcrTime = now;
        
        if (DEBUG) console.log('[Scanner] Running OCR');
        await performOCR(video, status, onDetected);
      }
    }, BARCODE_INTERVAL);
  }

  function stopDetectionLoop() {
    if (detectionFrame) {
      clearInterval(detectionFrame);
      detectionFrame = null;
      if (DEBUG) console.log('[Scanner] Detection loop stopped');
    }
  }

  function resetScanTimeout(status) {
    if (scanTimeout) clearTimeout(scanTimeout);
    scanTimeout = setTimeout(() => {
      if (status && !vinDetected) {
        status.textContent = 'Having trouble? Try manual entry above.';
        status.className = 'scan-status timeout';
      }
    }, 20000);
  }

  function onVINDetected(vin) {
    const guide = document.querySelector('.scan-guide');
    const status = document.getElementById('scanStatus');

    if (vinDetected) return;
    vinDetected = true;

    if (scanTimeout) {
      clearTimeout(scanTimeout);
      scanTimeout = null;
    }

    stopDetectionLoop();
    stopQuagga();
    stopZXing();
    stopCamera();

    if (guide) guide.classList.add('success');
    status.textContent = `VIN Detected: ${vin}`;
    status.className = 'scan-status success';

    setTimeout(() => {
      closeScanner();
      const vinInput = document.getElementById('vinInput');
      if (vinInput) {
        vinInput.value = vin;
        vinInput.dispatchEvent(new Event('input'));
        if (typeof decodeVIN === 'function') {
          decodeVIN();
        }
      }
    }, 500);
  }

  function getErrorMessage(error) {
    const isIos = isIOS();
    
    switch (error) {
      case 'not-supported':
        return 'Camera not supported on this browser';
      case 'permission-denied':
        if (isIos) {
          return 'Camera access denied. Go to Settings → Safari → Camera & Microphone → Allow';
        }
        return 'Camera access denied. Please allow camera access in browser settings → Site permissions';
      case 'permission-denied-ios':
        return 'Camera access denied.\n\nOn iOS: Tap the "aA" button → Website Settings → Allow Camera\n\nOr: Go to Settings → Safari → Camera & Microphone → Allow';
      case 'autoplay-blocked':
        if (isIos) {
          return 'Video autoplay blocked. Try tapping the scan button again.';
        }
        return 'Video autoplay blocked. Please allow video autoplay in browser settings.';
      case 'not-found':
        return 'No camera found on this device';
      case 'camera-in-use':
        return 'Camera is in use by another app. Please close other apps and try again.';
      case 'camera-failed':
        return 'Unable to start camera. Please ensure no other app is using the camera.';
      default:
        return 'Camera error. Please try again.';
    }
  }

  async function openScanner() {
    const modal = document.getElementById('scanModal');
    const status = document.getElementById('scanStatus');
    const guide = document.querySelector('.scan-guide');

    if (DEBUG) console.log('[Scanner] === openScanner START ===');
    
    if (!isMobileOrTablet()) {
      status.textContent = 'Scanner is only available on mobile devices';
      status.className = 'scan-status error';
      modal.classList.add('active');
      return;
    }

    modal.classList.add('active');
    if (guide) guide.classList.remove('success');
    status.textContent = 'Initializing scanner...';
    status.className = 'scan-status';
    
    vinDetected = false;
    nativeCameraActive = false;
    quaggaActive = false;
    resetScanTimeout(status);

    const isIos = isIOS();
    if (DEBUG) console.log('[Scanner] openScanner: iOS:', isIos, 'User Agent:', navigator.userAgent);

    if (DEBUG) console.log('[Scanner] Loading libraries...');
    try {
      const loadPromises = [loadTesseract()];
      if (!window.adapter) {
        loadPromises.push(loadWebRTCAdapter());
      }
      await Promise.all(loadPromises);
      if (DEBUG) console.log('[Scanner] Libraries loaded');
    } catch (e) {
      status.textContent = 'Failed to load scanner. Please try again.';
      status.className = 'scan-status error';
      if (DEBUG) console.error('[Scanner] Library load failed:', e);
      return;
    }

    if (isScanning) {
      closeScanner();
    }

    if (DEBUG) console.log('[Scanner] Checking native BarcodeDetector support:', hasNativeBarcodeDetector());

    const cameraResult = await initCamera(status);
    if (!cameraResult.success) {
      status.textContent = getErrorMessage(cameraResult.error);
      status.className = 'scan-status error';
      if (DEBUG) console.error('[Scanner] Camera initialization failed:', cameraResult.error);
      return;
    }

    await startDetectionLoop(status, onVINDetected);
    isScanning = true;
    
    if (DEBUG) console.log('[Scanner] Scanner started successfully');
  }

  async function closeScanner() {
    const modal = document.getElementById('scanModal');
    const status = document.getElementById('scanStatus');
    const guide = document.querySelector('.scan-guide');
    const video = document.getElementById('scanViewport');

    if (scanTimeout) {
      clearTimeout(scanTimeout);
      scanTimeout = null;
    }

    stopDetectionLoop();
    stopQuagga();
    stopZXing();
    stopCamera();
    await terminateTesseractWorker();
    
    lastOcrTime = 0;
    isScanning = false;
    vinDetected = false;

    if (video) {
      try {
        video.pause();
        video.removeAttribute('src');
        video.load();
      } catch (e) {
        if (DEBUG) console.error('[Scanner] Error cleaning up video:', e);
      }
    }

    if (guide) guide.classList.remove('success');
    status.textContent = '';
    status.className = 'scan-status';
    modal.classList.remove('active');
    
    if (DEBUG) console.log('[Scanner] Scanner closed');
  }

  function init() {
    const scanBtn = document.getElementById('scanBtn');
    const closeBtn = document.getElementById('scanClose');
    const modal = document.getElementById('scanModal');

    if (scanBtn) {
      scanBtn.addEventListener('click', openScanner);
      scanBtn.addEventListener('touchend', (e) => {
        e.preventDefault();
        e.stopPropagation();
        openScanner();
      });
    }

    if (closeBtn) {
      closeBtn.addEventListener('click', closeScanner);
      closeBtn.addEventListener('touchend', (e) => {
        e.preventDefault();
        closeScanner();
      });
    }

    if (modal) {
      modal.addEventListener('click', (e) => {
        if (e.target === modal) {
          closeScanner();
        }
      });
      modal.addEventListener('touchend', (e) => {
        if (e.target === modal) {
          closeScanner();
        }
      });
    }

    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && modal && modal.classList.contains('active')) {
        closeScanner();
      }
    });

    showScanButton();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  return { open: openScanner, close: closeScanner };
})();
