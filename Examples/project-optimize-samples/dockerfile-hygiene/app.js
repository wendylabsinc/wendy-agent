const http = require("http");

http
  .createServer((_req, res) => res.end("ok"))
  .listen(process.env.PORT || 3000);
