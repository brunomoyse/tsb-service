
# tsb-service - A RESTful API in Go for a restaurant product management

**tsb-service**, a RESTful API built with **Go** (Golang) to serve as the backend of a **webshop** for a restaurant. The API provides essential functionalities for handling orders, managing products with multi-language support, and processing payments via **Mollie**.

## 🎯 Features

- **Order Management**: Handle customer orders efficiently with endpoints to create, retrieve, and manage orders.
- **Payment Integration with Mollie**: Seamlessly process payments using Mollie, a leading payment provider in Europe.
- **Multi-language Product Listings**: Support for multiple languages through the `product_translations` table, allowing you to list products in various languages without needing to modify the code.
- **Product Editing**: Easily update product information such as names, descriptions, and prices, allowing the restaurant to manage its menu dynamically.
- **Real-time Delivery Tracking (Future Feature)**: Plan to integrate **Gorilla WebSocket** for real-time order delivery updates.
- **GPS Delivery Tracking with Teltonika Trackers (Future Feature)**: Real-time tracking of delivery vehicles using Teltonika GPS trackers, allowing customers to follow their orders on a map.

---

## 🛠 Technologies Used

- **Go (Golang)**: Backend API implementation.
- **Mollie API**: For secure and smooth payment processing.
- **PostgreSQL**: Database used to store all relevant data.
- **Docker**: Containerization for easy deployment and environment consistency.
- **Gorilla WebSocket** *(v1+ Feature)*: For real-time order delivery updates.
- **Teltonika Trackers** *(v1+ Feature)*: To track deliveries on a map.

---

## 🚀 Getting Started

Follow the steps below to get the API running locally.

### 1. Clone the Repository

```bash
git clone https://github.com/brunomoyse/tsb-service.git
cd tsb-service
```

### 2. Setup Environment Variables

Create a copy of the `.env.example` file as `.env`:

```bash
cp .env.example .env
```

Edit the `.env` file to configure the following environment variables:

```
DB_HOST=your_database_host
DB_PORT=your_database_port
DB_USERNAME=your_database_username
DB_PASSWORD=your_database_password
DB_DATABASE=your_database_name

MOLLIE_API_TOKEN=your_mollie_api_token
APP_BASE_URL=http://localhost:8080 # used for mollie redirects
JWT_SECRET=your_jwt_secret
```

### 3. Install Dependencies

```bash
go mod tidy
```

### 4. Run the Application

The main entry point of the application is located in the `src` directory.

```bash
cd src
go run main.go
```

This will start the API locally on `http://localhost:8080` (or another port you define in the environment variables).

### 5. Running with Docker Compose (Optional)

If you prefer to run the application inside Docker containers:

1. Ensure you have **Docker** and **Docker Compose** installed.
2. Run the following command to spin up the containers:

```bash
docker-compose up --build
```

This will start your Go API and any other services (like PostgreSQL) that are configured in your `docker-compose.yml` file.

---

## 📄 API Endpoints

### Order Management

- **Create Order**: `POST /orders`
- **Get Orders**: `GET /orders`
- **Get Order by ID**: `GET /orders/{id}`
  
### Product Management (Multi-language Supported)

- **Get Products**: `GET /products` (Returns products based on the user's language using the `product_translations` table, supporting multiple languages without code changes)
- **Edit Products**: `PUT /products/{id}` (Update product information dynamically)

### Payment Integration

- **Initiate Payment**: Payments are handled through the Mollie API with `POST /payments`.

More detailed API documentation will be available in future releases.

---

## 🌐 Planned Features (v1+)

### Real-time Order Delivery Tracking

- **Gorilla WebSocket**: Plan to implement real-time updates using WebSocket technology to notify customers when their order is being delivered.
  
### Delivery Tracking with Teltonika GPS

- Integration with **Teltonika GPS trackers** to follow delivery vehicles live on a map. Customers can view the status and location of their orders in real-time.

---

## 📦 Project Structure

```bash
tsb-service/
├── docker-compose.yml  # Docker configuration
├── .env.example        # Environment variable example file
├── README.md           # Project documentation
└── src/
    ├── main.go         # Main entry point of the Go API
    ├── controllers/    # API endpoint logic
    ├── models/         # Database models
    ├── routes/         # API routes definition
    └── middleware/     # Authentication and other middleware
```

---

## 📚 Useful Commands

### Build the Docker Image

```bash
docker build -t tsb-service .
```

### Run Unit Tests

```bash
go test ./...
```
