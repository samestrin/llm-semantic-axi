const fs = require("fs");

function loadConfig() {
    const io = require("./util/io.js");
    const helper = require("./a.test");
    return io.read(helper);
}
