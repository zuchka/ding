export default {
  async fetch() {
    const scriptUrl =
      "https://raw.githubusercontent.com/zuchka/ding/main/scripts/install.sh";
    const response = await fetch(scriptUrl);
    return new Response(response.body, {
      headers: {
        "content-type": "text/plain; charset=utf-8",
        "cache-control": "no-cache",
      },
    });
  },
};
