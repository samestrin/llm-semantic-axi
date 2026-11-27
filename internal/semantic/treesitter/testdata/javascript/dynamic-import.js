function boot() {
    import("./m.js").then(m => m.go());
    return import("react");
}
