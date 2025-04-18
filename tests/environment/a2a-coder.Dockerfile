FROM node:lts-alpine

WORKDIR /app

# Install git for cloning the repository
RUN apk add --no-cache git

# Clone the A2A repository
RUN git clone https://github.com/google/A2A.git

# Change to the A2A directory
WORKDIR /app/A2A/samples/js

# Install dependencies
RUN npm install

# Expose the default port for the coder agent
EXPOSE 41241

# Start the coder agent
CMD ["sh", "-c", "npm run agents:coder"] 