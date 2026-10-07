const agent = require("./agent");

module.exports = async () => {
  agent.build();
  await agent.start();
  return async () => agent.stop();
};
