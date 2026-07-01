(function () {
  var key = 'wendy_docs_analytics_consent';
  var analyticsConsent = 'denied';

  function isConsentState(value) {
    return value === 'granted' || value === 'denied';
  }

  function readConsentCookie() {
    var prefix = key + '=';
    var cookies = document.cookie ? document.cookie.split('; ') : [];

    for (var index = 0; index < cookies.length; index += 1) {
      var cookie = cookies[index];
      if (cookie.indexOf(prefix) !== 0) continue;

      try {
        return decodeURIComponent(cookie.slice(prefix.length));
      } catch (error) {
        return cookie.slice(prefix.length);
      }
    }

    return null;
  }

  try {
    var stored = window.localStorage.getItem(key);
    var cookieValue = readConsentCookie();

    if (isConsentState(stored)) {
      analyticsConsent = stored;
    } else if (isConsentState(cookieValue)) {
      analyticsConsent = cookieValue;
    }
  } catch (error) {}

  window.dataLayer = window.dataLayer || [];
  window.gtag =
    window.gtag ||
    function () {
      window.dataLayer.push(arguments);
    };
  window.gtag('consent', 'default', {
    analytics_storage: analyticsConsent,
    ad_storage: 'denied',
    ad_user_data: 'denied',
    ad_personalization: 'denied',
  });
})();
