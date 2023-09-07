# tsb-service/Dockerfile
FROM php:8.2-fpm

# Install Composer
COPY --from=composer:latest /usr/bin/composer /usr/bin/composer

# Install dependencies
RUN apt-get update && apt-get install -y \
    libfreetype6-dev \
    libjpeg62-turbo-dev \
    libpng-dev \
    libpq-dev \
    git \
    unzip \
    p7zip-full \
    zlib1g-dev \
    libzip-dev \
    && docker-php-ext-configure gd --with-freetype --with-jpeg \
    && docker-php-ext-install gd pgsql pdo pdo_pgsql zip

# Set working directory
WORKDIR /var/www/tsb-service

# Copy existing application directory contents
COPY . /var/www/tsb-service

# Install Laravel dependencies
RUN composer install

# Set permissions for web server
RUN chown -R www-data:www-data /var/www/tsb-service

CMD ["php-fpm"]
