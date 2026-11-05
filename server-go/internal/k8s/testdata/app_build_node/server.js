const http = require("http");
const ms = require("ms");
http.createServer((req, res) => res.end("hello from the app build fixture, " + ms(60000) + "\n")).listen(process.env.PORT || 8080);
