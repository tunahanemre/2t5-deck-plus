// the old editor lives behind ?legacy=1 (an external file: the page allows no inline script)
if (new URLSearchParams(location.search).has("legacy")) location.replace("./legacy/index.html" + location.search);
